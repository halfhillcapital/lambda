package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"sync"
	"time"

	"lambda/internal/config"
)

// Logger writes one JSON object per line to its underlying writer. All
// methods are safe to call on a nil receiver. Each Write goes to the kernel
// before returning, so a crash loses at most the in-flight call.
type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// debugLogFile is the fixed path used when --debug is set. Relative to the
// working directory, so it lands inside the worktree alongside other session
// artifacts.
const debugLogFile = "debug.jsonl"

// logBodyMax caps the size of body fields (tool results, assistant content,
// reasoning) included in log records so a single huge tool result doesn't
// blow up the JSONL file. The full size is always recorded separately as a
// `*_chars` field, so truncation is detectable.
const logBodyMax = 4096

// truncBody returns s capped at logBodyMax with a marker if truncated.
func truncBody(s string) string {
	if len(s) <= logBodyMax {
		return s
	}
	return s[:logBodyMax] + "…<truncated>"
}

// openLogger opens path in append mode and returns a Logger. If opening fails
// the error is returned and the caller should continue without logging.
func openLogger(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	return &Logger{w: f}, nil
}

// OpenDebugLog opens the debug log file when cfg.Debug is set. Returns
// (nil, nil) when debug logging is disabled, so callers can pass the result
// straight to New without branching.
func OpenDebugLog(cfg *config.Config) (*Logger, error) {
	if !cfg.Debug {
		return nil, nil
	}
	return openLogger(debugLogFile)
}

// Close releases the underlying writer if it's an io.Closer (e.g. the *os.File
// returned by openLogger). Safe to call on a nil receiver.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	if c, ok := l.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Write emits one JSONL record with the given kind and fields. ts is filled in
// automatically. A nil Logger is a no-op so callers don't need to guard.
func (l *Logger) Write(kind string, fields map[string]any) {
	if l == nil {
		return
	}
	rec := make(map[string]any, len(fields)+2)
	rec["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	rec["kind"] = kind
	maps.Copy(rec, fields)
	b, err := json.Marshal(rec)
	if err != nil {
		b, _ = json.Marshal(map[string]any{
			"ts":   rec["ts"],
			"kind": kind,
			"_err": err.Error(),
		})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

// eventFields returns a map describing an Event for the JSONL log. One-shot
// events (tool_start, tool_result) include their body verbatim up to
// logBodyMax; high-volume streaming events (content_delta, thinking_delta)
// only record a char count to keep the log readable — the full content
// surfaces in the corresponding `response` record.
func eventFields(e Event) (string, map[string]any) {
	switch x := e.(type) {
	case EventContentDelta:
		return "content_delta", map[string]any{"chars": len(x.Text)}
	case EventThinkingDelta:
		return "thinking_delta", map[string]any{"chars": len(x.Text)}
	case EventAssistantDone:
		return "assistant_done", map[string]any{"chars": len(x.Text)}
	case EventToolStart:
		return "tool_start", map[string]any{"id": x.ID, "name": x.Name, "args": x.Args}
	case EventToolResult:
		return "tool_result", map[string]any{"id": x.ID, "name": x.Name, "result": truncBody(x.Result), "chars": len(x.Result)}
	case EventToolDenied:
		return "tool_denied", map[string]any{"id": x.ID, "name": x.Name}
	case EventContextUsage:
		return "context_usage", map[string]any{"used": x.Used, "limit": x.Limit}
	case EventTurnDone:
		return "turn_done", map[string]any{"reason": x.Reason}
	case EventError:
		return "error", map[string]any{"err": x.Err.Error()}
	}
	return "event_unknown", map[string]any{"type": fmt.Sprintf("%T", e)}
}
