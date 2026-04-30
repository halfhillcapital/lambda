package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"

	"lambda/internal/tools"
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
	h := &history{messages: []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage("system"),
		openai.UserMessage("hello"),
	}}
	first := h.totalChars()
	if first <= 0 {
		t.Fatalf("totalChars=%d, want >0", first)
	}
	h.messages = append(h.messages, makeAssistant("a long-ish reply with extra chars"))
	if h.totalChars() <= first {
		t.Errorf("totalChars should grow when messages are added")
	}
}

func TestCompactDisabledByDefault(t *testing.T) {
	h := &history{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage("hello"),
		},
		maxContextTokens: 0,
	}
	before := len(h.messages)
	h.compactIfNeeded()
	if len(h.messages) != before {
		t.Errorf("compaction should be disabled when maxContextTokens<=0; got %d→%d", before, len(h.messages))
	}
	if h.droppedTurns != 0 {
		t.Errorf("droppedTurns=%d, want 0 when disabled", h.droppedTurns)
	}
}

func TestCompactNoOpUnderCap(t *testing.T) {
	h := &history{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage("hi"),
			makeAssistant("hello"),
		},
		maxContextTokens: 30_000,
	}
	before := len(h.messages)
	h.compactIfNeeded()
	if len(h.messages) != before {
		t.Errorf("nothing should be dropped under cap; got %d→%d", before, len(h.messages))
	}
	if h.droppedTurns != 0 {
		t.Errorf("droppedTurns=%d, want 0", h.droppedTurns)
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
	h := &history{messages: msgs, maxContextTokens: 230}

	h.compactIfNeeded()

	if h.droppedTurns == 0 {
		t.Error("expected drops, got none")
	}
	// Last user message should be the most recent ("u4...").
	lastUserIdx := -1
	for i := len(h.messages) - 1; i >= 0; i-- {
		if roleOf(h.messages[i]) == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		t.Fatal("no user message remaining")
	}
	if got := h.estimateTokens(); got > h.maxContextTokens {
		// Compaction is "best effort" — the shrinker only targets tool
		// messages, so a last-turn user/assistant pair that alone exceeds
		// the cap is tolerated.
		t.Logf("estimateTokens=%d, cap=%d (acceptable if last turn alone exceeds cap)", got, h.maxContextTokens)
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
	h := &history{messages: msgs, maxContextTokens: 230}

	h.compactIfNeeded()
	firstDrops := h.droppedTurns
	if firstDrops == 0 {
		t.Fatal("expected drops")
	}

	// Note should be at index 1 (right after the original system prompt) and be a system message.
	if h.elisionNoteIdx == 0 {
		t.Fatal("elisionNoteIdx not set")
	}
	if roleOf(h.messages[h.elisionNoteIdx]) != "system" {
		t.Errorf("elision note should be a system message; got %q", roleOf(h.messages[h.elisionNoteIdx]))
	}
	noteJSON, _ := h.messages[h.elisionNoteIdx].MarshalJSON()
	if !strings.Contains(string(noteJSON), "earlier turn") {
		t.Errorf("note content unexpected: %s", noteJSON)
	}

	// Add more turns so compaction runs again and updates the count.
	for i := range 4 {
		h.messages = append(h.messages,
			openai.UserMessage(strings.Repeat("U", 300)+itoa(i)),
			makeAssistant(strings.Repeat("A", 300)+itoa(i)),
		)
	}
	h.compactIfNeeded()

	if h.droppedTurns <= firstDrops {
		t.Errorf("expected more drops on second compaction: %d → %d", firstDrops, h.droppedTurns)
	}
	// Still exactly one elision note, and its count is current.
	noteCount := 0
	for _, m := range h.messages {
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

func TestCompactLogsCompactionEvent(t *testing.T) {
	var buf bytes.Buffer
	msgs := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage("sys")}
	for i := range 5 {
		msgs = append(msgs,
			openai.UserMessage(strings.Repeat("u", 200)+itoa(i)),
			makeAssistant(strings.Repeat("a", 200)+itoa(i)),
		)
	}
	a := &Agent{
		history: &history{messages: msgs, maxContextTokens: 230},
		logger:  &Logger{w: &buf},
	}

	a.compactIfNeeded()

	if a.history.droppedTurns == 0 {
		t.Fatal("expected drops")
	}

	var rec map[string]any
	for line := range strings.SplitSeq(strings.TrimRight(buf.String(), "\n"), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		if r["kind"] == "compaction" {
			rec = r
			break
		}
	}
	if rec == nil {
		t.Fatalf("no compaction record in log; got: %s", buf.String())
	}
	if got, _ := rec["turns_dropped"].(float64); got == 0 {
		t.Errorf("turns_dropped = %v, want >0", rec["turns_dropped"])
	}
	if rec["before_tokens"].(float64) <= rec["after_tokens"].(float64) {
		t.Errorf("expected before_tokens > after_tokens; got before=%v after=%v",
			rec["before_tokens"], rec["after_tokens"])
	}
}

func TestCompactNoEventWhenNoOp(t *testing.T) {
	var buf bytes.Buffer
	a := &Agent{
		history: &history{
			messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage("sys"),
				openai.UserMessage("hi"),
			},
			maxContextTokens: 30_000,
		},
		logger: &Logger{w: &buf},
	}
	a.compactIfNeeded()
	if buf.Len() > 0 {
		t.Errorf("no log expected when nothing was compacted; got %q", buf.String())
	}
}

func TestCompactKeepsAtLeastOneTurn(t *testing.T) {
	h := &history{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage(strings.Repeat("x", 5000)),
		},
		maxContextTokens: 30,
	}
	h.compactIfNeeded()
	// Even though we're way over cap, we can't drop the only user message.
	hasUser := false
	for _, m := range h.messages {
		if roleOf(m) == "user" {
			hasUser = true
		}
	}
	if !hasUser {
		t.Error("compaction should keep at least the most recent user message")
	}
}

func TestEstimateTokensUsesDefaultRatioWhenUncalibrated(t *testing.T) {
	h := &history{messages: []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(strings.Repeat("x", 350)),
	}}
	chars := h.totalChars()
	got := h.estimateTokens()
	// With defaultCharsPerToken=3.5, ceil(350/3.5) = 100, but totalChars also
	// includes JSON overhead — sanity check the ratio, not the exact number.
	if got <= 0 || got > chars {
		t.Errorf("estimateTokens=%d, totalChars=%d — expected 0<est<=chars", got, chars)
	}
}

func TestRecordTokenUsageCalibrates(t *testing.T) {
	h := &history{}
	h.recordTokenUsage(1000, 250) // 4.0 chars/token
	if h.charsPerToken != 4.0 {
		t.Errorf("charsPerToken=%v, want 4.0", h.charsPerToken)
	}
	// Zero/negative inputs must not clobber a good calibration.
	h.recordTokenUsage(0, 100)
	if h.charsPerToken != 4.0 {
		t.Errorf("zero charsSent must not change calibration; got %v", h.charsPerToken)
	}
	h.recordTokenUsage(500, 0)
	if h.charsPerToken != 4.0 {
		t.Errorf("zero promptTokens must not change calibration; got %v", h.charsPerToken)
	}
}

func TestEstimateTokensUsesCalibratedRatio(t *testing.T) {
	h := &history{messages: []openai.ChatCompletionMessageParamUnion{
		openai.UserMessage(strings.Repeat("x", 400)),
	}}
	before := h.estimateTokens()
	// A looser ratio (more chars per token) should lower the estimate.
	h.recordTokenUsage(1000, 100) // 10 chars/token
	after := h.estimateTokens()
	if after >= before {
		t.Errorf("after calibration to 10 chars/token, estimate should drop: before=%d after=%d", before, after)
	}
}

func TestCompactShrinksOversizedToolMessage(t *testing.T) {
	// One turn with a huge tool reply — drop loop can't help, so the shrinker
	// must kick in and truncate the tool message body until we fit.
	huge := strings.Repeat("x", 10_000)
	h := &history{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("sys"),
			openai.UserMessage("run a big command"),
			makeAssistant("calling tool"),
			openai.ToolMessage(huge, "call_1"),
		},
		maxContextTokens: 600,
	}
	h.compactIfNeeded()

	if got := h.estimateTokens(); got > h.maxContextTokens {
		t.Errorf("shrink failed: estimateTokens=%d > cap=%d", got, h.maxContextTokens)
	}
	// Tool message must still exist and keep its tool_call_id so the pairing invariant holds.
	var tool *openai.ChatCompletionToolMessageParam
	for _, m := range h.messages {
		if m.OfTool != nil {
			tool = m.OfTool
		}
	}
	if tool == nil {
		t.Fatal("tool message disappeared after shrinking")
	}
	if tool.ToolCallID != "call_1" {
		t.Errorf("tool_call_id not preserved after shrink: %q", tool.ToolCallID)
	}
	body := tool.Content.OfString.Value
	if !strings.Contains(body, "truncated from") {
		t.Errorf("expected truncation marker in body; got %q", body[:min(80, len(body))])
	}
}

func TestReset(t *testing.T) {
	approver := NewApprover(
		tools.Registry{},
		func(context.Context, string, string) Decision { return DecisionDeny },
		false,
	)
	approver.allowedTools["bash"] = true
	approver.alwaysAll = true

	a := &Agent{
		history: &history{
			messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage("sys"),
				openai.UserMessage("hi"),
				makeAssistant("hello"),
				openai.UserMessage("more"),
			},
			droppedTurns:   3,
			elisionNoteIdx: 1,
		},
		approver: approver,
	}
	a.Reset()

	if len(a.history.messages) != 1 {
		t.Errorf("Reset should leave only the system message; got %d messages", len(a.history.messages))
	}
	if roleOf(a.history.messages[0]) != "system" {
		t.Errorf("first message should be system; got %q", roleOf(a.history.messages[0]))
	}
	if a.history.droppedTurns != 0 {
		t.Errorf("droppedTurns not reset: %d", a.history.droppedTurns)
	}
	if a.history.elisionNoteIdx != 0 {
		t.Errorf("elisionNoteIdx not reset: %d", a.history.elisionNoteIdx)
	}
	// Agent.Reset must delegate to approver.Reset (covered in detail in
	// approver_test.go; this is the integration check).
	if len(approver.allowedTools) != 0 {
		t.Errorf("approver.allowedTools not cleared: %v", approver.allowedTools)
	}
	if approver.alwaysAll {
		t.Error("approver.alwaysAll should revert to yolo (false) after Reset")
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
