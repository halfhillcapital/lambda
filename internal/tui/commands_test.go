package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lambda/internal/skills"
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
