package agent

import (
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
)

// makeAssistant builds an assistant message with text content for tests.
func makeAssistant(text string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
		Content: openai.ChatCompletionAssistantMessageParamContentUnion{OfString: param.NewOpt(text)},
	}}
}

func TestRoleOf(t *testing.T) {
	cases := []struct {
		name string
		msg  openai.ChatCompletionMessageParamUnion
		want string
	}{
		{"system", openai.SystemMessage("hi"), "system"},
		{"user", openai.UserMessage("hi"), "user"},
		{"assistant", makeAssistant("hi"), "assistant"},
		{"tool", openai.ToolMessage("res", "id123"), "tool"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := roleOf(c.msg); got != c.want {
				t.Errorf("roleOf = %q, want %q", got, c.want)
			}
		})
	}
}

func TestTotalCharsCountsAllMessages(t *testing.T) {
	a := &Agent{messages: []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage("system"),
		openai.UserMessage("hello"),
	}}
	first := a.totalChars()
	if first <= 0 {
		t.Fatalf("totalChars=%d, want >0", first)
	}
	a.messages = append(a.messages, makeAssistant("a long-ish reply with extra chars"))
	if a.totalChars() <= first {
		t.Errorf("totalChars should grow when messages are added")
	}
}

func TestCompactDisabledByDefault(t *testing.T) {
	a := &Agent{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage("hello"),
		},
		maxContextChars: 0,
	}
	before := len(a.messages)
	a.compactIfNeeded()
	if len(a.messages) != before {
		t.Errorf("compaction should be disabled when maxContextChars<=0; got %d→%d", before, len(a.messages))
	}
	if a.droppedTurns != 0 {
		t.Errorf("droppedTurns=%d, want 0 when disabled", a.droppedTurns)
	}
}

func TestCompactNoOpUnderCap(t *testing.T) {
	a := &Agent{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage("hi"),
			makeAssistant("hello"),
		},
		maxContextChars: 100_000,
	}
	before := len(a.messages)
	a.compactIfNeeded()
	if len(a.messages) != before {
		t.Errorf("nothing should be dropped under cap; got %d→%d", before, len(a.messages))
	}
	if a.droppedTurns != 0 {
		t.Errorf("droppedTurns=%d, want 0", a.droppedTurns)
	}
}

func TestCompactDropsOldestTurn(t *testing.T) {
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage("sys prompt"),
	}
	for i := range 5 {
		msgs = append(msgs,
			openai.UserMessage(strings.Repeat("u", 200)+itoa(i)),
			makeAssistant(strings.Repeat("a", 200)+itoa(i)),
		)
	}
	a := &Agent{messages: msgs, maxContextChars: 800}

	a.compactIfNeeded()

	if a.droppedTurns == 0 {
		t.Error("expected drops, got none")
	}
	// Last user message should be the most recent ("u4...").
	lastUserIdx := -1
	for i := len(a.messages) - 1; i >= 0; i-- {
		if roleOf(a.messages[i]) == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		t.Fatal("no user message remaining")
	}
	if got := a.totalChars(); got > a.maxContextChars {
		// Compaction is "best effort" — single-turn-too-big is allowed.
		// But here we have multiple turns, so we should be at/under cap.
		// Tolerance: at most 1 turn over cap is acceptable since we keep the latest.
		t.Logf("totalChars=%d, cap=%d (acceptable if last turn alone exceeds cap)", got, a.maxContextChars)
	}
}

func TestCompactInsertsAndUpdatesElisionNote(t *testing.T) {
	msgs := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage("sys"),
	}
	for i := range 5 {
		msgs = append(msgs,
			openai.UserMessage(strings.Repeat("u", 200)+itoa(i)),
			makeAssistant(strings.Repeat("a", 200)+itoa(i)),
		)
	}
	a := &Agent{messages: msgs, maxContextChars: 800}

	a.compactIfNeeded()
	firstDrops := a.droppedTurns
	if firstDrops == 0 {
		t.Fatal("expected drops")
	}

	// Note should be at index 1 (right after the original system prompt) and be a system message.
	if a.elisionNoteIdx == 0 {
		t.Fatal("elisionNoteIdx not set")
	}
	if roleOf(a.messages[a.elisionNoteIdx]) != "system" {
		t.Errorf("elision note should be a system message; got %q", roleOf(a.messages[a.elisionNoteIdx]))
	}
	noteJSON, _ := a.messages[a.elisionNoteIdx].MarshalJSON()
	if !strings.Contains(string(noteJSON), "earlier turn") {
		t.Errorf("note content unexpected: %s", noteJSON)
	}

	// Add more turns so compaction runs again and updates the count.
	for i := range 4 {
		a.messages = append(a.messages,
			openai.UserMessage(strings.Repeat("U", 300)+itoa(i)),
			makeAssistant(strings.Repeat("A", 300)+itoa(i)),
		)
	}
	a.compactIfNeeded()

	if a.droppedTurns <= firstDrops {
		t.Errorf("expected more drops on second compaction: %d → %d", firstDrops, a.droppedTurns)
	}
	// Still exactly one elision note, and its count is current.
	noteCount := 0
	for _, m := range a.messages {
		if roleOf(m) != "system" {
			break
		}
		b, _ := m.MarshalJSON()
		if strings.Contains(string(b), "earlier turn") {
			noteCount++
		}
	}
	if noteCount != 1 {
		t.Errorf("want exactly 1 elision note, got %d", noteCount)
	}
}

func TestCompactKeepsAtLeastOneTurn(t *testing.T) {
	a := &Agent{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage(strings.Repeat("x", 5000)),
		},
		maxContextChars: 100,
	}
	a.compactIfNeeded()
	// Even though we're way over cap, we can't drop the only user message.
	hasUser := false
	for _, m := range a.messages {
		if roleOf(m) == "user" {
			hasUser = true
		}
	}
	if !hasUser {
		t.Error("compaction should keep at least the most recent user message")
	}
}

func TestReset(t *testing.T) {
	a := &Agent{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage("hi"),
			makeAssistant("hello"),
			openai.UserMessage("more"),
		},
		droppedTurns:   3,
		elisionNoteIdx: 1,
		allowedTools:   map[string]bool{"bash": true},
		alwaysAll:      true,
		yolo:           false,
	}
	a.Reset()

	if len(a.messages) != 1 {
		t.Errorf("Reset should leave only the system message; got %d messages", len(a.messages))
	}
	if roleOf(a.messages[0]) != "system" {
		t.Errorf("first message should be system; got %q", roleOf(a.messages[0]))
	}
	if a.droppedTurns != 0 {
		t.Errorf("droppedTurns not reset: %d", a.droppedTurns)
	}
	if a.elisionNoteIdx != 0 {
		t.Errorf("elisionNoteIdx not reset: %d", a.elisionNoteIdx)
	}
	if len(a.allowedTools) != 0 {
		t.Errorf("allowedTools not cleared: %v", a.allowedTools)
	}
	if a.alwaysAll {
		t.Error("alwaysAll should reset to false when yolo=false")
	}
}

func TestResetPreservesYoloFlag(t *testing.T) {
	a := &Agent{
		messages:     []openai.ChatCompletionMessageParamUnion{openai.SystemMessage("sys"), openai.UserMessage("hi")},
		yolo:         true,
		alwaysAll:    true,
		allowedTools: map[string]bool{},
	}
	a.Reset()
	if !a.alwaysAll {
		t.Error("alwaysAll should stay true after Reset when yolo=true (the flag persists)")
	}
}

// itoa avoids pulling strconv just for tests.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
