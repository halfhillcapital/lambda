package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"lambda/internal/config"
	"lambda/internal/tools"
)

// recordedRequest holds the body of one request the fake server received.
// Messages is decoded as raw JSON per element so tests can peek at role and
// tool_call_id without depending on the openai-go union shape.
type recordedRequest struct {
	Raw      []byte
	Messages []json.RawMessage
}

// scriptedServer is a fake OpenAI-compatible Chat Completions endpoint. Each
// inbound request is captured, then `respond` is invoked with the call index
// (0-based) to produce the status and JSON body to return.
type scriptedServer struct {
	*httptest.Server
	t       *testing.T
	respond func(call int, req recordedRequest) (status int, body any)

	mu   sync.Mutex
	reqs []recordedRequest
}

func newScriptedServer(t *testing.T, respond func(int, recordedRequest) (int, any)) *scriptedServer {
	t.Helper()
	s := &scriptedServer{t: t, respond: respond}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

func (s *scriptedServer) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("read request body: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var top struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &top)
	rec := recordedRequest{Raw: body, Messages: top.Messages}

	s.mu.Lock()
	idx := len(s.reqs)
	s.reqs = append(s.reqs, rec)
	s.mu.Unlock()

	status, resp := s.respond(idx, rec)
	if chunks, ok := resp.(streamingBody); ok {
		writeSSE(w, status, chunks)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.t.Errorf("encode response: %v", err)
	}
}

// streamingBody, when returned from a respond callback, signals the handler to
// emit an SSE response. Each entry is one chunk's JSON body; the handler
// frames them as `data: <json>\n\n` and appends a final `data: [DONE]\n\n`.
type streamingBody []map[string]any

func writeSSE(w http.ResponseWriter, status int, chunks streamingBody) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	flusher, _ := w.(http.Flusher)
	for _, c := range chunks {
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// sseChunk builds one chat.completion.chunk body. Pass the per-chunk delta
// fields (content, role, tool_calls, ...). finish="" leaves finish_reason
// null; otherwise sets it (typically only on the final non-usage chunk).
func sseChunk(delta map[string]any, finish string) map[string]any {
	choice := map[string]any{
		"index":         0,
		"delta":         delta,
		"finish_reason": nil,
	}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "test",
		"choices": []map[string]any{choice},
	}
}

// sseUsageChunk builds the trailing usage-only chunk emitted when the request
// passed stream_options.include_usage:true. choices is empty per the OpenAI
// spec for usage chunks.
func sseUsageChunk(promptTokens int64) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "test",
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": 0,
			"total_tokens":      promptTokens,
		},
	}
}

func (s *scriptedServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *scriptedServer) request(i int) recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[i]
}

type fakeToolCall struct {
	ID, Name, Arguments string
}

// cannedAssistant builds a ChatCompletion JSON body containing a single
// assistant choice. finish is the OpenAI finish_reason ("stop", "tool_calls",
// "length", ...). promptTokens populates usage.prompt_tokens; the agent uses
// that value to calibrate its char-to-token ratio.
func cannedAssistant(text, finish string, promptTokens int64, tcs ...fakeToolCall) map[string]any {
	msg := map[string]any{
		"role":    "assistant",
		"content": text,
	}
	if len(tcs) > 0 {
		out := make([]map[string]any, len(tcs))
		for i, tc := range tcs {
			out[i] = map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			}
		}
		msg["tool_calls"] = out
	}
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 0,
		"model":   "test",
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": 0,
			"total_tokens":      promptTokens,
		},
	}
}

// cannedError builds an OpenAI-shaped error envelope. The SDK constructs a
// typed *openai.Error from this — required for isTransient/humanizeError to
// match the typed-error code paths.
func cannedError(msg string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "server_error",
		},
	}
}

// newAgent wires an Agent against the fake server in non-streaming mode with
// the default registry and a confirmer that fails the test if called.
// Suitable for tests that don't exercise the destructive-tool confirmation
// flow (read is auto-allow; bash defers to its allowlist).
func newAgent(t *testing.T, srv *scriptedServer, mutate ...func(*config.Config)) *Agent {
	t.Helper()
	confirmer := func(ctx context.Context, name, args string) Decision {
		t.Fatalf("confirmer called unexpectedly: name=%s args=%s", name, args)
		return DecisionDeny
	}
	return newAgentFull(t, srv, tools.New(""), confirmer, mutate...)
}

// newAgentFull is newAgent with an explicit registry and confirmer; used by
// tests that exercise the confirmation flow.
func newAgentFull(t *testing.T, srv *scriptedServer, registry tools.Registry, confirmer Confirmer, mutate ...func(*config.Config)) *Agent {
	t.Helper()
	cfg := &config.Config{
		BaseURL:  srv.URL,
		APIKey:   "test",
		Model:    "test",
		MaxSteps: 10,
		NoStream: true,
	}
	for _, m := range mutate {
		m(cfg)
	}
	logger, err := OpenDebugLog(cfg)
	if err != nil {
		t.Fatalf("OpenDebugLog: %v", err)
	}
	approver := NewApprover(registry, confirmer, cfg.Yolo)
	return New(cfg, "sys", registry, approver, logger)
}

// drainEvents collects every event from out until the channel closes.
func drainEvents(out <-chan Event) []Event {
	var events []Event
	for ev := range out {
		events = append(events, ev)
	}
	return events
}
