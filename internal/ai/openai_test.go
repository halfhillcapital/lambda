package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/respjson"
)

func TestIsTransient(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"500", &openai.Error{StatusCode: 500}, true},
		{"503", &openai.Error{StatusCode: 503}, true},
		{"599", &openai.Error{StatusCode: 599}, true},
		{"408 request timeout", &openai.Error{StatusCode: 408}, true},
		{"429 rate limit", &openai.Error{StatusCode: 429}, true},
		{"400 bad request", &openai.Error{StatusCode: 400}, false},
		{"401 auth", &openai.Error{StatusCode: 401}, false},
		{"404 not found", &openai.Error{StatusCode: 404}, false},
		{"io.EOF", io.EOF, true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"net.OpError", &net.OpError{Op: "read", Err: errors.New("dial fail")}, true},
		{"ECONNRESET wrapped", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{"plain error", errors.New("nope"), false},
		{"context canceled", context.Canceled, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTransient(c.err); got != c.want {
				t.Errorf("isTransient(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestJitter(t *testing.T) {
	const base = 1000 * time.Millisecond
	for range 100 {
		got := jitter(base)
		if got < 750*time.Millisecond || got > 1250*time.Millisecond {
			t.Fatalf("jitter(%v) = %v, want within ±25%%", base, got)
		}
	}
	if got := jitter(0); got != 0 {
		t.Errorf("jitter(0) = %v, want 0", got)
	}
}

func TestWithTransientRetry(t *testing.T) {
	withFastBackoffs(t, 1*time.Millisecond, 1*time.Millisecond)

	t.Run("succeeds first try without retry", func(t *testing.T) {
		calls := 0
		res, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			return 42, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if res != 42 {
			t.Errorf("got %d", res)
		}
		if calls != 1 {
			t.Errorf("called %d times, want 1", calls)
		}
	})

	t.Run("retries on transient then succeeds", func(t *testing.T) {
		calls := 0
		res, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			if calls < 3 {
				return 0, &openai.Error{StatusCode: 503}
			}
			return 7, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if res != 7 {
			t.Errorf("got %d", res)
		}
		if calls != 3 {
			t.Errorf("called %d times, want 3", calls)
		}
	})

	t.Run("does not retry non-transient", func(t *testing.T) {
		calls := 0
		_, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			return 0, &openai.Error{StatusCode: 401}
		})
		if err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Errorf("called %d times, want 1", calls)
		}
	})

	t.Run("gives up after exhausting retries", func(t *testing.T) {
		calls := 0
		_, err := withTransientRetry(context.Background(), func() (int, error) {
			calls++
			return 0, &openai.Error{StatusCode: 500}
		})
		if err == nil {
			t.Fatal("expected error after exhausting retries")
		}
		want := len(retryBackoffs) + 1
		if calls != want {
			t.Errorf("called %d times, want %d", calls, want)
		}
	})

	t.Run("respects ctx cancellation during backoff", func(t *testing.T) {
		// Use longer backoffs so ctx cancellation lands during the sleep.
		saved := retryBackoffs
		retryBackoffs = []time.Duration{500 * time.Millisecond, 500 * time.Millisecond}
		defer func() { retryBackoffs = saved }()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		calls := 0
		start := time.Now()
		_, err := withTransientRetry(ctx, func() (int, error) {
			calls++
			return 0, &openai.Error{StatusCode: 500}
		})
		if err == nil {
			t.Error("expected error")
		}
		if calls > 2 {
			t.Errorf("retried after ctx cancel: %d calls", calls)
		}
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("took %v, expected to bail out near ctx deadline", elapsed)
		}
	})
}

func TestExtractReasoning(t *testing.T) {
	cases := []struct {
		name   string
		extras map[string]respjson.Field
		want   string
	}{
		{
			name:   "nil extras",
			extras: nil,
			want:   "",
		},
		{
			name: "reasoning_content set",
			extras: map[string]respjson.Field{
				"reasoning_content": respjson.NewField(`"step one"`),
			},
			want: "step one",
		},
		{
			name: "thinking field (ollama)",
			extras: map[string]respjson.Field{
				"thinking": respjson.NewField(`"deliberating"`),
			},
			want: "deliberating",
		},
		{
			name: "both populated — concatenated",
			extras: map[string]respjson.Field{
				"reasoning_content": respjson.NewField(`"a"`),
				"thinking":          respjson.NewField(`"b"`),
			},
			want: "ab",
		},
		{
			name: "null is ignored",
			extras: map[string]respjson.Field{
				"reasoning_content": respjson.NewField("null"),
			},
			want: "",
		},
		{
			name: "unrelated extras ignored",
			extras: map[string]respjson.Field{
				"some_other_field": respjson.NewField(`"x"`),
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractReasoning(tc.extras); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// captureServer records the body of one request and replies with a minimal
// canned chat completion. Used to assert what we splice into the wire format.
func captureServer(t *testing.T, respUsage map[string]any) (*httptest.Server, *[]byte) {
	t.Helper()
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		captured = body
		usage := map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		for k, v := range respUsage {
			usage[k] = v
		}
		resp := map[string]any{
			"id": "x", "object": "chat.completion", "created": 0, "model": "test",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": usage,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, b)
	}
	return m
}

func TestRequestShaping_OpenAICompat(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenAICompat, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "test",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	if _, ok := got["provider"]; ok {
		t.Errorf("openai-compat must not splice provider object")
	}
	if _, ok := got["usage"]; ok {
		t.Errorf("openai-compat must not request detailed usage")
	}
	if _, ok := got["reasoning"]; ok {
		t.Errorf("reasoning omitted when ReasoningOff")
	}
}

func TestRequestShaping_OpenRouter_DefaultUsageOptIn(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenRouter, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "anthropic/claude",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	usage, ok := got["usage"].(map[string]any)
	if !ok {
		t.Fatalf("openrouter must opt in to detailed usage; body=%s", *body)
	}
	if usage["include"] != true {
		t.Errorf("usage.include = %v, want true", usage["include"])
	}
	if _, ok := got["provider"]; ok {
		t.Errorf("provider object should be omitted when no routing prefs are set")
	}
}

func TestRequestShaping_OpenRouter_ProviderRouting(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenRouter, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "anthropic/claude",
		Messages: []Message{UserMessage("hi")},
		Provider: ProviderConfig{DenyDataCollection: true, NoFallbacks: true},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	prov, ok := got["provider"].(map[string]any)
	if !ok {
		t.Fatalf("missing provider object; body=%s", *body)
	}
	if prov["data_collection"] != "deny" {
		t.Errorf("data_collection = %v, want deny", prov["data_collection"])
	}
	if prov["allow_fallbacks"] != false {
		t.Errorf("allow_fallbacks = %v, want false", prov["allow_fallbacks"])
	}
}

func TestRequestShaping_Reasoning(t *testing.T) {
	srv, body := captureServer(t, nil)
	c := NewOpenAICompleter(ProviderOpenAICompat, srv.URL, "k", false)
	_, err := c.Complete(context.Background(), CompletionRequest{
		Model:     "test",
		Messages:  []Message{UserMessage("hi")},
		Reasoning: ReasoningHigh,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeBody(t, *body)
	r, ok := got["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("missing reasoning object; body=%s", *body)
	}
	if r["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, want high", r["effort"])
	}
}

func TestCostExtraction_OpenRouter(t *testing.T) {
	srv, _ := captureServer(t, map[string]any{"cost": 0.0123})
	c := NewOpenAICompleter(ProviderOpenRouter, srv.URL, "k", false)
	res, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "anthropic/claude",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cost != 0.0123 {
		t.Errorf("cost = %v, want 0.0123", res.Cost)
	}
}

func TestCostExtraction_OpenAICompatIgnoresCostField(t *testing.T) {
	srv, _ := captureServer(t, map[string]any{"cost": 0.0123})
	c := NewOpenAICompleter(ProviderOpenAICompat, srv.URL, "k", false)
	res, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "test",
		Messages: []Message{UserMessage("hi")},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cost != 0 {
		t.Errorf("openai-compat must not surface cost; got %v", res.Cost)
	}
}

func TestStreamingRetriesTransientErrorBeforeFirstChunk(t *testing.T) {
	withFastBackoffs(t, 1*time.Millisecond, 1*time.Millisecond)

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{
					"message": "temporary overload",
					"type":    "server_error",
				},
			})
			return
		}
		writeStreamingCompletion(w,
			streamChunk(map[string]any{"role": "assistant", "content": "ok"}, "stop"),
			streamUsageChunk(3),
		)
	}))
	t.Cleanup(srv.Close)

	c := NewOpenAICompleter(ProviderOpenAICompat, srv.URL, "k", true)
	var deltas []string
	res, err := c.Complete(context.Background(), CompletionRequest{
		Model:    "test",
		Messages: []Message{UserMessage("hi")},
	}, func(s string) {
		deltas = append(deltas, s)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if res.Message.Content != "ok" {
		t.Errorf("content = %q, want ok", res.Message.Content)
	}
	if strings.Join(deltas, "") != "ok" {
		t.Errorf("deltas = %v, want [ok]", deltas)
	}
}

func streamChunk(delta map[string]any, finish string) map[string]any {
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

func streamUsageChunk(promptTokens int64) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "test",
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": 1,
			"total_tokens":      promptTokens + 1,
		},
	}
}

func writeStreamingCompletion(w http.ResponseWriter, chunks ...map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, chunk := range chunks {
		b, _ := json.Marshal(chunk)
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

// withFastBackoffs swaps retryBackoffs to 1ms delays for the duration of the
// test, restoring the original on cleanup.
func withFastBackoffs(t *testing.T, delays ...time.Duration) {
	t.Helper()
	saved := retryBackoffs
	retryBackoffs = delays
	t.Cleanup(func() { retryBackoffs = saved })
}
