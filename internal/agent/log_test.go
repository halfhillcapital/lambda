package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lambda/internal/config"
)

func TestLogger_NilIsNoop(t *testing.T) {
	var l *Logger
	// Must not panic.
	l.Write("anything", map[string]any{"x": 1})
	kind, fields := eventFields(EventTurnDone{Reason: "done"})
	l.Write(kind, fields)
}

func TestLogger_WritesOneJSONLineWithKindAndTimestamp(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf}

	l.Write("response", map[string]any{
		"finish_reason": "tool_calls",
		"tool_calls":    2,
	})

	out := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(out, "\n") {
		t.Fatalf("expected single line, got %q", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("unmarshal: %v (line=%q)", err, out)
	}
	if rec["kind"] != "response" {
		t.Errorf("kind = %v, want response", rec["kind"])
	}
	if rec["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason = %v", rec["finish_reason"])
	}
	if _, ok := rec["ts"].(string); !ok {
		t.Errorf("ts missing or not string: %v", rec["ts"])
	}
}

func TestEventFields_CoversAllEvents(t *testing.T) {
	cases := []struct {
		ev       Event
		wantKind string
	}{
		{EventContentDelta{Text: "abc"}, "content_delta"},
		{EventThinkingDelta{Text: "abc"}, "thinking_delta"},
		{EventAssistantDone{Text: "abc"}, "assistant_done"},
		{EventToolStart{ID: "1", Name: "ls", Args: "{}"}, "tool_start"},
		{EventToolResult{ID: "1", Name: "ls", Result: "abc"}, "tool_result"},
		{EventToolDenied{ID: "1", Name: "rm"}, "tool_denied"},
		{EventTurnDone{Reason: "done"}, "turn_done"},
		{EventError{Err: errors.New("boom")}, "error"},
	}
	for _, c := range cases {
		got, _ := eventFields(c.ev)
		if got != c.wantKind {
			t.Errorf("eventFields(%T) kind = %q, want %q", c.ev, got, c.wantKind)
		}
	}
}

// TestRun_LogFile_CapturesFinishReason wires a JSONL log file through the full
// agent loop against the fake server and asserts the finish_reason from each
// completion lands in a "response" record. This is the user-visible payoff of
// the logging work — without it there's no way to debug "tool call without
// final answer" cases after the fact.
func TestRun_LogFile_CapturesFinishReason(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(srcPath, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := newScriptedServer(t, func(call int, _ recordedRequest) (int, any) {
		if call == 0 {
			return 200, cannedAssistant("", "tool_calls", 10,
				fakeToolCall{ID: "call_1", Name: "read", Arguments: readFileArgs(srcPath)})
		}
		return 200, cannedAssistant("done", "stop", 20)
	})

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	logPath := filepath.Join(dir, debugLogFile)
	a := newAgent(t, srv, func(c *config.Config) { c.Debug = true })
	out := make(chan Event, 32)
	a.Run(context.Background(), "read it", out)
	drainEvents(out)
	// Release the log file handle so TempDir cleanup works on Windows.
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	var responses []map[string]any
	var kinds []string
	for line := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		kinds = append(kinds, rec["kind"].(string))
		if rec["kind"] == "response" {
			responses = append(responses, rec)
		}
	}

	for _, want := range []string{"session_start", "turn_start", "request", "response", "tool_start", "tool_result", "turn_done"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("log missing kind %q; saw %v", want, kinds)
		}
	}

	if len(responses) != 2 {
		t.Fatalf("got %d response records, want 2", len(responses))
	}
	if got := responses[0]["finish_reason"]; got != "tool_calls" {
		t.Errorf("first response finish_reason = %v, want tool_calls", got)
	}
	if got := responses[1]["finish_reason"]; got != "stop" {
		t.Errorf("second response finish_reason = %v, want stop", got)
	}
	// The final assistant turn produced "done" — we should see that text in the
	// content body, not just a char count. This pins the body-inclusion fix.
	if got := responses[1]["content"]; got != "done" {
		t.Errorf("second response content = %q, want %q", got, "done")
	}
}

func TestTruncBody_TruncatesAndMarks(t *testing.T) {
	// Below the cap → returned verbatim.
	short := strings.Repeat("a", logBodyMax)
	if got := truncBody(short); got != short {
		t.Errorf("at-cap input was modified")
	}
	// Above the cap → truncated to logBodyMax + marker, full size still
	// recoverable from a separate `*_chars` field at the call site.
	long := strings.Repeat("a", logBodyMax+100)
	got := truncBody(long)
	if !strings.HasSuffix(got, "<truncated>") {
		t.Errorf("expected truncation marker, got suffix %q", got[len(got)-20:])
	}
	if !strings.HasPrefix(got, short) {
		t.Errorf("truncated output should start with the first logBodyMax bytes")
	}
}

func TestOpenLogger_AppendsBetweenSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")

	for i := range 2 {
		l, err := openLogger(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		l.Write("session_start", map[string]any{"i": i})
		if err := l.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), b)
	}
}
