package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lambda/internal/ai"
)

func TestHistoryRecordMessageAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	h := newHistory(path)

	h.RecordMessage(ai.SystemMessage("dropped: system messages aren't persisted"))
	h.RecordMessage(ai.UserMessage("hello"))
	h.RecordMessage(ai.Message{
		Role:    ai.RoleAssistant,
		Content: "running a tool",
		ToolCalls: []ai.ToolCall{
			{ID: "call_1", Name: "read", Arguments: `{"path":"a"}`},
		},
	})
	h.RecordMessage(ai.ToolMessage("file body", "call_1"))

	records := readHistory(t, path)
	if len(records) != 3 {
		t.Fatalf("expected 3 records (system message dropped), got %d:\n%v", len(records), records)
	}
	if records[0].Role != ai.RoleUser || records[0].Content != "hello" {
		t.Errorf("record[0]=%+v", records[0])
	}
	if records[1].Role != ai.RoleAssistant || len(records[1].ToolCalls) != 1 || records[1].ToolCalls[0].ID != "call_1" {
		t.Errorf("record[1]=%+v", records[1])
	}
	if records[2].Role != ai.RoleTool || records[2].ToolCallID != "call_1" || records[2].Content != "file body" {
		t.Errorf("record[2]=%+v", records[2])
	}
}

func TestHistoryRecordResetTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	h := newHistory(path)

	h.RecordMessage(ai.UserMessage("first conversation"))
	h.RecordReset()
	h.RecordMessage(ai.UserMessage("second conversation"))

	records := readHistory(t, path)
	if len(records) != 1 || records[0].Content != "second conversation" {
		t.Errorf("expected only post-reset record, got %+v", records)
	}
}

func TestEphemeralHistoryDoesNotTouchDisk(t *testing.T) {
	dir := t.TempDir()
	h := newHistory("")
	h.RecordMessage(ai.UserMessage("nothing on disk"))
	h.RecordReset()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("ephemeral history wrote files: %v", entries)
	}
}

func readHistory(t *testing.T, path string) []historyRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	defer f.Close()
	var out []historyRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r historyRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}
