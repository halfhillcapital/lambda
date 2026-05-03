package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lambda/internal/agent"
	"lambda/internal/prompt"
	"lambda/internal/skills"
	"lambda/internal/tools"
)

func TestSlashCommandDispatcher_ResetCommands(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(skills.Empty())

	for _, input := range []string{"/new", "/clear"} {
		result := dispatcher.Dispatch(input)
		if result.kind != slashCommandReset {
			t.Fatalf("Dispatch(%q).kind=%v, want reset", input, result.kind)
		}
		if len(result.notices) != 1 || result.notices[0] != "started a new conversation" {
			t.Fatalf("Dispatch(%q).notices=%q, want reset notice", input, result.notices)
		}
	}
}

func TestSlashCommandDispatcher_HelpListsSkills(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(testSkillIndex(t))

	result := dispatcher.Dispatch("/help")

	if result.kind != slashCommandHelp {
		t.Fatalf("kind=%v, want help", result.kind)
	}
	if len(result.notices) != 2 {
		t.Fatalf("len(notices)=%d, want 2", len(result.notices))
	}
	if !strings.Contains(result.notices[1], "/plan") {
		t.Fatalf("skill help=%q, want /plan", result.notices[1])
	}
}

func TestSlashCommandDispatcher_SkillStartsTurn(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(testSkillIndex(t))

	result := dispatcher.Dispatch("/plan use ADRs")

	if result.kind != slashCommandStartTurn {
		t.Fatalf("kind=%v, want start turn", result.kind)
	}
	if !strings.Contains(result.startInput, "<command-name>/plan</command-name>") {
		t.Fatalf("startInput=%q, want skill command name", result.startInput)
	}
	if !strings.Contains(result.startInput, "<command-args>use ADRs</command-args>") {
		t.Fatalf("startInput=%q, want skill args", result.startInput)
	}
}

func TestSlashCommandDispatcher_Context(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(skills.Empty())
	result := dispatcher.Dispatch("/context")
	if result.kind != slashCommandShowContext {
		t.Fatalf("kind=%v, want show context", result.kind)
	}
}

func TestSlashCommandDispatcher_Worktree(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(skills.Empty())
	result := dispatcher.Dispatch("/worktree")
	if result.kind != slashCommandShowWorktree {
		t.Fatalf("kind=%v, want show worktree", result.kind)
	}
}

func TestSlashCommandDispatcher_Merge(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(skills.Empty())
	result := dispatcher.Dispatch("/merge")
	if result.kind != slashCommandMerge {
		t.Fatalf("kind=%v, want merge", result.kind)
	}
}

func TestSlashCommandDispatcher_BuiltinsWinOverSkills(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(testSkillIndex(t))

	result := dispatcher.Dispatch("/clear should not invoke skill")

	if result.kind != slashCommandReset {
		t.Fatalf("kind=%v, want reset", result.kind)
	}
}

func TestSlashCommandDispatcher_UnknownCommand(t *testing.T) {
	dispatcher := newSlashCommandDispatcher(skills.Empty())

	result := dispatcher.Dispatch("/missing")

	if result.kind != slashCommandUnknown {
		t.Fatalf("kind=%v, want unknown", result.kind)
	}
	if result.err != "unknown command: /missing (try /help)" {
		t.Fatalf("err=%q, want unknown command error", result.err)
	}
}

func testSkillIndex(t *testing.T) *skills.Index {
	t.Helper()
	root := t.TempDir()
	writeSkill(t, root, "plan", "Plan carefully")
	writeSkill(t, root, "clear", "Skill shadowed by builtin")
	return skills.Load([]string{root}, nil)
}

func writeSkill(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nBody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- Context command tests ---

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
