package tui

import (
	"strings"
	"testing"

	"lambda/internal/agent"
	"lambda/internal/prompt"
	"lambda/internal/tools"
)

func TestRenderContextCommand_BreakdownContents(t *testing.T) {
	snap := agent.ContextSnapshot{
		SystemPromptChars: 1000,
		UserChars:         200, UserMsgs: 1,
		AssistantChars: 300, AssistantMsgs: 1,
		ToolChars: 500, ToolMsgs: 1,
		MaxContextTokens: 32000,
		CharsPerToken:    3.5,
		Calibrated:       false,
	}
	sections := prompt.Sections{
		Base:        "BASE",
		Environment: "ENV",
		Project:     "## Project instructions\n\nfrom AGENTS.md\n\nbody",
		Skills:      "<available-skills>\n\n- a: x\n- b: y\n</available-skills>",
	}
	breakdown, dump := renderContextCommand(snap, sections, tools.Registry{})

	for _, want := range []string{
		"context:", "tokens", "(default)",
		"base instructions", "environment", "project context",
		"(AGENTS.md)", "skills listing", "(2 skills)",
		"tool schemas", "messages",
		"user", "assistant", "tool",
		"elision note", "total",
	} {
		if !strings.Contains(breakdown, want) {
			t.Errorf("breakdown missing %q\n--breakdown--\n%s", want, breakdown)
		}
	}
	if !strings.Contains(dump, "--- system prompt ---") || !strings.Contains(dump, "--- end ---") {
		t.Errorf("dump missing fences:\n%s", dump)
	}
	if !strings.Contains(dump, "BASE") || !strings.Contains(dump, "ENV") {
		t.Errorf("dump missing section bodies:\n%s", dump)
	}
}

func TestRenderContextCommand_UncappedAndCalibrated(t *testing.T) {
	snap := agent.ContextSnapshot{
		MaxContextTokens: 0,
		CharsPerToken:    4.2,
		Calibrated:       true,
	}
	breakdown, _ := renderContextCommand(snap, prompt.Sections{Base: "x"}, tools.Registry{})
	if !strings.Contains(breakdown, "uncapped") {
		t.Errorf("expected 'uncapped' label when MaxContextTokens=0; got:\n%s", breakdown)
	}
	if !strings.Contains(breakdown, "(calibrated)") {
		t.Errorf("expected '(calibrated)' label; got:\n%s", breakdown)
	}
}
