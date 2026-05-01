package tui

import (
	"strings"
	"testing"

	"lambda/internal/agent"
)

func TestTranscript_ContentDeltaFinalizesThinking(t *testing.T) {
	txn := newTranscript(nil)

	txn.ApplyAgentEvent(agent.EventThinkingDelta{Text: "checking options"})
	txn.ApplyAgentEvent(agent.EventContentDelta{Text: "answer"})

	if len(txn.blocks) != 2 {
		t.Fatalf("len(blocks)=%d, want 2", len(txn.blocks))
	}
	if !txn.blocks[0].final {
		t.Fatalf("thinking block was not finalized")
	}
	if txn.blocks[1].kind != blockAssistant || txn.blocks[1].text != "answer" {
		t.Fatalf("assistant block=%+v, want content delta", txn.blocks[1])
	}
}

func TestTranscript_ToolResultCompletesOpenToolBlock(t *testing.T) {
	txn := newTranscript(func(name, rawArgs string) string {
		return name + " " + rawArgs
	})

	result := txn.ApplyAgentEvent(agent.EventToolStart{Name: "read", Args: `{"file":"x"}`})
	txn.ApplyAgentEvent(agent.EventToolResult{Name: "read", Result: "line 1\nline 2"})

	if !result.toolStarted {
		t.Fatalf("toolStarted=false, want true")
	}
	if len(txn.blocks) != 1 {
		t.Fatalf("len(blocks)=%d, want 1", len(txn.blocks))
	}
	if !txn.blocks[0].final {
		t.Fatalf("tool block was not finalized")
	}
	if !strings.Contains(txn.blocks[0].text, "line 2") {
		t.Fatalf("tool block text=%q, want result text", txn.blocks[0].text)
	}
}

func TestTranscript_ContextUsageIsReturnedWithoutAppendingBlock(t *testing.T) {
	txn := newTranscript(nil)

	result := txn.ApplyAgentEvent(agent.EventContextUsage{Used: 1234, Limit: 4096})

	if !result.hasTokenUsage || result.tokenUsed != 1234 || result.tokenCap != 4096 {
		t.Fatalf("result=%+v, want token usage", result)
	}
	if len(txn.blocks) != 0 {
		t.Fatalf("len(blocks)=%d, want 0", len(txn.blocks))
	}
}
