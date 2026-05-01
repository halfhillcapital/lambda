package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"

	"lambda/internal/agent"
)

func TestHandleEvent_ContextUsageUpdatesModelState(t *testing.T) {
	m := &uiModel{
		viewport: viewport.New(80, 20),
	}

	m.handleEvent(agent.EventContextUsage{Used: 1234, Limit: 4096})

	if m.tokenUsed != 1234 {
		t.Fatalf("tokenUsed=%d, want 1234", m.tokenUsed)
	}
	if m.tokenCap != 4096 {
		t.Fatalf("tokenCap=%d, want 4096", m.tokenCap)
	}
}

func TestRenderTokenUsage_UsesCachedContextUsage(t *testing.T) {
	m := &uiModel{tokenUsed: 1234, tokenCap: 4096}

	got := m.renderTokenUsage()

	if !strings.Contains(got, "1.2k/4.1k tok") {
		t.Fatalf("renderTokenUsage()=%q, want label %q", got, "1.2k/4.1k tok")
	}
}

func TestRenderTokenUsage_NoCap(t *testing.T) {
	m := &uiModel{tokenUsed: 999, tokenCap: 0}

	got := m.renderTokenUsage()

	if !strings.Contains(got, "999 tok") {
		t.Fatalf("renderTokenUsage()=%q, want label %q", got, "999 tok")
	}
}

