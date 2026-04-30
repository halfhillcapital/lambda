package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lambda/internal/config"
	"lambda/internal/tools"
)

func readFileArgs(path string) string {
	b, _ := json.Marshal(map[string]string{"path": path})
	return string(b)
}

// TestRun_CancelMidTurn_PreservesPairing checks the pairing invariant on the
// cancellation path: when the model returns N tool calls and ctx is cancelled
// after the first one runs, the loop must still append a tool message for
// every tool_call_id (the OpenAI API 400s otherwise).
func TestRun_CancelMidTurn_PreservesPairing(t *testing.T) {
	dir := t.TempDir()
	readPath := filepath.Join(dir, "a.txt")
	writePath := filepath.Join(dir, "b.txt")
	thirdPath := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(readPath, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newScriptedServer(t, func(call int, _ recordedRequest) (int, any) {
		writeArgs, _ := json.Marshal(map[string]string{"path": writePath, "content": "x"})
		return 200, cannedAssistant("", "tool_calls", 0,
			fakeToolCall{ID: "call_1", Name: "read", Arguments: readFileArgs(readPath)},
			fakeToolCall{ID: "call_2", Name: "write", Arguments: string(writeArgs)},
			fakeToolCall{ID: "call_3", Name: "read", Arguments: readFileArgs(thirdPath)},
		)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	confirmer := func(_ context.Context, _, _ string) Decision {
		cancel()
		return DecisionAllow
	}

	a := newAgentFull(t, srv, tools.New(""), confirmer)
	out := make(chan Event, 64)
	a.Run(ctx, "go", out)
	events := drainEvents(out)

	if got := srv.calls(); got != 1 {
		t.Errorf("server calls = %d, want 1 (no follow-up after cancel)", got)
	}

	asstIdx := -1
	for i, m := range a.history.messages {
		if m.OfAssistant != nil && len(m.OfAssistant.ToolCalls) > 0 {
			asstIdx = i
			break
		}
	}
	if asstIdx < 0 {
		t.Fatal("no assistant message with tool_calls in history")
	}
	asst := a.history.messages[asstIdx].OfAssistant
	asstIDs := map[string]bool{}
	for _, tc := range asst.ToolCalls {
		asstIDs[tc.ID] = true
	}

	toolIDs := map[string]bool{}
	placeholders := 0
	for _, m := range a.history.messages[asstIdx+1:] {
		if m.OfTool == nil {
			continue
		}
		toolIDs[m.OfTool.ToolCallID] = true
		if m.OfTool.Content.OfString.Value == "cancelled by user" {
			placeholders++
		}
	}

	if !reflect.DeepEqual(asstIDs, toolIDs) {
		t.Errorf("tool_call_id mismatch: assistant=%v tool=%v", asstIDs, toolIDs)
	}
	if placeholders < 1 {
		t.Errorf("expected ≥1 'cancelled by user' placeholder, got %d", placeholders)
	}

	for _, ev := range events {
		if e, ok := ev.(EventError); ok {
			t.Errorf("unexpected EventError: %v", e.Err)
		}
	}
}

// TestRun_RetryExhaustion_EmitsEventError verifies that a persistently failing
// upstream produces exactly one EventError after exhausting retries, that the
// error message is humanized, and that no assistant message is committed.
func TestRun_RetryExhaustion_EmitsEventError(t *testing.T) {
	withFastBackoffs(t, 1*time.Millisecond, 1*time.Millisecond)

	srv := newScriptedServer(t, func(call int, _ recordedRequest) (int, any) {
		return 500, cannedError("upstream boom")
	})

	a := newAgent(t, srv)
	out := make(chan Event, 16)
	a.Run(context.Background(), "go", out)
	events := drainEvents(out)

	wantCalls := len(retryBackoffs) + 1
	if got := srv.calls(); got != wantCalls {
		t.Errorf("server calls = %d, want %d (one initial + %d retries)", got, wantCalls, len(retryBackoffs))
	}

	if len(events) != 1 {
		t.Fatalf("got %d events, want exactly 1 (EventError): %+v", len(events), events)
	}
	errEv, ok := events[0].(EventError)
	if !ok {
		t.Fatalf("event is %T, want EventError", events[0])
	}
	msg := errEv.Err.Error()
	if !strings.Contains(msg, "api error (500)") {
		t.Errorf("err missing 'api error (500)': %q", msg)
	}
	if !strings.Contains(msg, "upstream boom") {
		t.Errorf("err missing 'upstream boom': %q", msg)
	}

	if n := len(a.history.messages); n == 0 || roleOf(a.history.messages[n-1]) != "user" {
		var roles []string
		for _, m := range a.history.messages {
			roles = append(roles, roleOf(m))
		}
		t.Errorf("history doesn't end at user message; roles=%v", roles)
	}
}

// TestRun_CompactionPreservesToolPairs drives the agent through three turns
// with intentionally large tool results so compaction's shrink phase fires
// before the final request. The test asserts on the wire-level body of that
// final request: every assistant tool_call_id has a matching tool message
// tool_call_id, and at least one tool body shows the truncation marker
// (proving shrink ran rather than being a no-op).
func TestRun_CompactionPreservesToolPairs(t *testing.T) {
	dir := t.TempDir()
	bigPath := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(strings.Repeat("x", 3000)), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newScriptedServer(t, func(call int, _ recordedRequest) (int, any) {
		switch call {
		case 0:
			return 200, cannedAssistant("", "tool_calls", 0,
				fakeToolCall{ID: "A", Name: "read", Arguments: readFileArgs(bigPath)})
		case 1:
			return 200, cannedAssistant("", "tool_calls", 0,
				fakeToolCall{ID: "B", Name: "read", Arguments: readFileArgs(bigPath)})
		default:
			return 200, cannedAssistant("final", "stop", 0)
		}
	})

	a := newAgent(t, srv, func(c *config.Config) { c.MaxContextTokens = 800 })
	out := make(chan Event, 64)
	a.Run(context.Background(), "do work", out)
	drainEvents(out)

	if got := srv.calls(); got != 3 {
		t.Fatalf("server calls = %d, want 3", got)
	}

	req := srv.request(2)
	type wireToolCall struct {
		ID string `json:"id"`
	}
	type wireMessage struct {
		Role       string         `json:"role"`
		ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
	}

	asstIDs := map[string]bool{}
	toolIDs := map[string]bool{}
	sawTruncationMarker := false
	for _, raw := range req.Messages {
		var m wireMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode message %s: %v", raw, err)
		}
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				asstIDs[tc.ID] = true
			}
		case "tool":
			toolIDs[m.ToolCallID] = true
			if strings.Contains(string(raw), "tool result truncated from") {
				sawTruncationMarker = true
			}
		}
	}

	want := map[string]bool{"A": true, "B": true}
	if !reflect.DeepEqual(asstIDs, want) {
		t.Errorf("assistant tool_call_ids = %v, want %v", asstIDs, want)
	}
	if !reflect.DeepEqual(toolIDs, want) {
		t.Errorf("tool tool_call_ids = %v, want %v", toolIDs, want)
	}
	if !sawTruncationMarker {
		t.Errorf("no truncation marker in any tool message — compaction shrink phase did not fire")
	}
}

// TestRun_IterationLimit_EmitsTurnDone verifies that a model that calls tools
// indefinitely is bounded by MaxSteps and exits with the iteration-limit
// reason rather than looping forever.
func TestRun_IterationLimit_EmitsTurnDone(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(file, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	const maxSteps = 3
	srv := newScriptedServer(t, func(call int, _ recordedRequest) (int, any) {
		// Always demand another tool call; never finish.
		return 200, cannedAssistant("", "tool_calls", 0,
			fakeToolCall{ID: fmt.Sprintf("call_%d", call), Name: "read", Arguments: readFileArgs(file)})
	})

	a := newAgent(t, srv, func(c *config.Config) { c.MaxSteps = maxSteps })
	out := make(chan Event, 64)
	a.Run(context.Background(), "go", out)
	events := drainEvents(out)

	if got := srv.calls(); got != maxSteps {
		t.Errorf("server calls = %d, want %d", got, maxSteps)
	}

	last, ok := events[len(events)-1].(EventTurnDone)
	if !ok {
		t.Fatalf("last event = %T, want EventTurnDone", events[len(events)-1])
	}
	want := fmt.Sprintf("hit iteration limit (%d steps)", maxSteps)
	if last.Reason != want {
		t.Errorf("EventTurnDone.Reason = %q, want %q", last.Reason, want)
	}
	for _, ev := range events {
		if e, ok := ev.(EventError); ok {
			t.Errorf("unexpected EventError: %v", e.Err)
		}
	}
}

// TestRun_DestructiveDenied_PreservesPairing verifies that a confirmer
// returning DecisionDeny on a destructive tool call still results in (a) an
// EventToolDenied event and (b) a matching tool_message with the exact
// "user denied this tool call" placeholder — same pairing invariant as the
// cancel-mid-turn path, different code path.
func TestRun_DestructiveDenied_PreservesPairing(t *testing.T) {
	var confirmCalls int
	confirmer := func(ctx context.Context, name, args string) Decision {
		confirmCalls++
		return DecisionDeny
	}

	srv := newScriptedServer(t, func(call int, _ recordedRequest) (int, any) {
		switch call {
		case 0:
			return 200, cannedAssistant("", "tool_calls", 0,
				fakeToolCall{ID: "call_bash", Name: "bash", Arguments: `{"command":"rm -rf foo"}`})
		default:
			return 200, cannedAssistant("understood", "stop", 0)
		}
	})

	a := newAgentFull(t, srv, tools.New(""), confirmer)
	out := make(chan Event, 32)
	a.Run(context.Background(), "go", out)
	events := drainEvents(out)

	if confirmCalls != 1 {
		t.Errorf("confirmer called %d times, want 1", confirmCalls)
	}
	if got := srv.calls(); got != 2 {
		t.Errorf("server calls = %d, want 2 (initial + post-denial)", got)
	}

	var sawDenied bool
	for _, ev := range events {
		if d, ok := ev.(EventToolDenied); ok {
			sawDenied = true
			if d.ID != "call_bash" || d.Name != "bash" {
				t.Errorf("EventToolDenied = %+v, want ID=call_bash Name=bash", d)
			}
		}
		if _, ok := ev.(EventToolStart); ok {
			t.Errorf("unexpected EventToolStart on denied tool")
		}
	}
	if !sawDenied {
		t.Error("no EventToolDenied emitted")
	}

	var foundDenialMsg bool
	for _, m := range a.history.messages {
		if m.OfTool == nil {
			continue
		}
		if m.OfTool.ToolCallID == "call_bash" {
			if got := m.OfTool.Content.OfString.Value; got != "denied this tool call" {
				t.Errorf("denial tool message content = %q, want %q", got, "denied this tool call")
			}
			foundDenialMsg = true
		}
	}
	if !foundDenialMsg {
		t.Error("no tool message paired with call_bash — pairing invariant broken")
	}
}

// TestRun_Streaming_StitchesContentAndToolCalls drives the streaming code
// path: the SDK's accumulator reassembles content and tool_calls split across
// multiple SSE chunks. The test asserts that EventContentDelta arrives per
// chunk and that the stitched tool_call (function name + arguments arriving
// piecewise) executes correctly — the second turn's request body must contain
// the assembled tool_call_id paired with a tool message.
func TestRun_Streaming_StitchesContentAndToolCalls(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	pathArg := readFileArgs(file)
	// Split the JSON args at an arbitrary point to exercise the accumulator.
	splitAt := len(pathArg) / 2
	argsHead, argsTail := pathArg[:splitAt], pathArg[splitAt:]

	srv := newScriptedServer(t, func(call int, _ recordedRequest) (int, any) {
		switch call {
		case 0:
			return 200, streamingBody{
				sseChunk(map[string]any{"role": "assistant", "content": ""}, ""),
				sseChunk(map[string]any{"content": "Look"}, ""),
				sseChunk(map[string]any{"content": "ing..."}, ""),
				sseChunk(map[string]any{"tool_calls": []map[string]any{{
					"index":    0,
					"id":       "call_x",
					"type":     "function",
					"function": map[string]any{"name": "read"},
				}}}, ""),
				sseChunk(map[string]any{"tool_calls": []map[string]any{{
					"index":    0,
					"function": map[string]any{"arguments": argsHead},
				}}}, ""),
				sseChunk(map[string]any{"tool_calls": []map[string]any{{
					"index":    0,
					"function": map[string]any{"arguments": argsTail},
				}}}, ""),
				sseChunk(map[string]any{}, "tool_calls"),
				sseUsageChunk(0),
			}
		default:
			return 200, streamingBody{
				sseChunk(map[string]any{"role": "assistant", "content": "done."}, ""),
				sseChunk(map[string]any{}, "stop"),
				sseUsageChunk(0),
			}
		}
	})

	a := newAgent(t, srv, func(c *config.Config) { c.NoStream = false })
	out := make(chan Event, 64)
	a.Run(context.Background(), "go", out)
	events := drainEvents(out)

	if got := srv.calls(); got != 2 {
		t.Fatalf("server calls = %d, want 2", got)
	}

	var deltas []string
	var sawToolStart, sawToolResult bool
	for _, ev := range events {
		switch e := ev.(type) {
		case EventContentDelta:
			deltas = append(deltas, e.Text)
		case EventToolStart:
			sawToolStart = true
			if e.ID != "call_x" || e.Name != "read" {
				t.Errorf("EventToolStart = %+v, want ID=call_x Name=read", e)
			}
			if e.Args != pathArg {
				t.Errorf("stitched tool_call args = %q, want %q", e.Args, pathArg)
			}
		case EventToolResult:
			sawToolResult = true
			if e.Result != "hi" {
				t.Errorf("tool result = %q, want %q (stitched args reached the tool)", e.Result, "hi")
			}
		}
	}

	wantDeltas := []string{"Look", "ing...", "done."}
	if !reflect.DeepEqual(deltas, wantDeltas) {
		t.Errorf("EventContentDelta sequence = %v, want %v", deltas, wantDeltas)
	}
	if !sawToolStart {
		t.Error("no EventToolStart — accumulator failed to surface stitched tool_call")
	}
	if !sawToolResult {
		t.Error("no EventToolResult — tool didn't execute")
	}

	// Wire-level: the second request must carry the assembled tool_call_id on
	// the assistant message and a paired tool message.
	req := srv.request(1)
	type wireToolCall struct {
		ID string `json:"id"`
	}
	type wireMessage struct {
		Role       string         `json:"role"`
		ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
	}
	asstIDs := map[string]bool{}
	toolIDs := map[string]bool{}
	for _, raw := range req.Messages {
		var m wireMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		switch m.Role {
		case "assistant":
			for _, tc := range m.ToolCalls {
				asstIDs[tc.ID] = true
			}
		case "tool":
			toolIDs[m.ToolCallID] = true
		}
	}
	want := map[string]bool{"call_x": true}
	if !reflect.DeepEqual(asstIDs, want) || !reflect.DeepEqual(toolIDs, want) {
		t.Errorf("turn-2 wire pairing: assistant=%v tool=%v, want both = %v", asstIDs, toolIDs, want)
	}
}
