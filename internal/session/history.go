package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lambda/internal/ai"
)

// History owns the on-disk message log at
// .lambda/sessions/<id>/history.jsonl. A Session holds one of these and
// hands it to the agent via Session.History(); the agent calls
// RecordMessage / RecordReset as it appends and resets its in-memory
// log. Errors are reported to stderr and swallowed: persistence
// failure must not break the agent loop.
//
// An ephemeral History (Path == "") is a valid no-op — oneshot runs
// can hand one to the agent without special-casing nil.
type History struct {
	path string
}

// HistoryPath returns .lambda/sessions/<id>/history.jsonl. Empty inputs
// yield "".
func HistoryPath(repoRoot, id string) string {
	if repoRoot == "" || id == "" {
		return ""
	}
	return filepath.Join(SessionsDir(repoRoot), id, "history.jsonl")
}

// newHistory builds a History rooted at the given file path. An empty
// path yields a no-op History (oneshot / unpersisted sessions).
func newHistory(path string) *History {
	return &History{path: path}
}

// Path returns the on-disk path of this history, or "" for an
// ephemeral History.
func (h *History) Path() string {
	if h == nil {
		return ""
	}
	return h.path
}

// historyRecord is the durable per-line shape written to history.jsonl.
// Decoupled from ai.Message so we can evolve the on-disk format without
// dragging the wire-format struct along.
type historyRecord struct {
	Role       ai.Role       `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []historyCall `json:"tool_calls,omitempty"`
}

type historyCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func recordFromMessage(m ai.Message) historyRecord {
	r := historyRecord{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		r.ToolCalls = make([]historyCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			r.ToolCalls[i] = historyCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
		}
	}
	return r
}

// RecordMessage appends one message to the on-disk log. No-op for
// ephemeral histories. System messages aren't persisted: the system
// prompt is rebuilt fresh on every resume from project context, so
// storing it would just waste disk and risk drift.
func (h *History) RecordMessage(m ai.Message) {
	if h == nil || h.path == "" || m.Role == ai.RoleSystem {
		return
	}
	if err := h.appendRecord(recordFromMessage(m)); err != nil {
		fmt.Fprintln(os.Stderr, "lambda: history append failed:", err)
	}
}

// RecordReset truncates the on-disk log. Called on /new-style soft
// resets so the next conversation starts from a fresh log without a
// dangling tail of pre-reset messages.
func (h *History) RecordReset() {
	if h == nil || h.path == "" {
		return
	}
	if err := h.truncate(); err != nil {
		fmt.Fprintln(os.Stderr, "lambda: history reset failed:", err)
	}
}

func (h *History) appendRecord(r historyRecord) error {
	if h.path == "" {
		return errors.New("history: empty path")
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("history: open: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

func (h *History) truncate() error {
	if err := os.Truncate(h.path, 0); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("history: truncate: %w", err)
	}
	return nil
}
