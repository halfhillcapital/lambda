package agent

import (
	"testing"

	"lambda/internal/ai"
)

func TestLoadReplayStripsToolBlocks(t *testing.T) {
	h := newHistory("sys", 0, 0)
	a := &Agent{history: h}

	a.LoadReplay([]ai.Message{
		ai.UserMessage("first"),
		{
			Role:    ai.RoleAssistant,
			Content: "let me look",
			ToolCalls: []ai.ToolCall{
				{ID: "call_1", Name: "read", Arguments: `{}`},
			},
		},
		ai.ToolMessage("file body", "call_1"),
		{Role: ai.RoleAssistant}, // tool-call-only assistant; should be dropped after stripping
		ai.AssistantMessage("the answer"),
		ai.UserMessage("follow-up"),
	})

	got := a.history.messages
	// system + 4 surviving messages (first user, "let me look", "the answer", follow-up)
	if len(got) != 5 {
		t.Fatalf("expected 5 messages (system + 4 replayed), got %d: %+v", len(got), got)
	}
	if got[0].Role != ai.RoleSystem {
		t.Errorf("system prompt overwritten: %+v", got[0])
	}
	if got[1].Role != ai.RoleUser || got[1].Content != "first" {
		t.Errorf("user[0]=%+v", got[1])
	}
	if got[2].Role != ai.RoleAssistant || got[2].Content != "let me look" || len(got[2].ToolCalls) != 0 {
		t.Errorf("tool_calls not stripped: %+v", got[2])
	}
	if got[3].Role != ai.RoleAssistant || got[3].Content != "the answer" {
		t.Errorf("answer[0]=%+v", got[3])
	}
	if got[4].Role != ai.RoleUser || got[4].Content != "follow-up" {
		t.Errorf("user[1]=%+v", got[4])
	}
}
