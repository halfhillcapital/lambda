package tui

import (
	"strings"

	"lambda/internal/skills"
)

type slashCommandKind int

const (
	slashCommandUnknown slashCommandKind = iota
	slashCommandReset
	slashCommandHelp
	slashCommandStartTurn
)

type slashCommandResult struct {
	kind       slashCommandKind
	startInput string
	notices    []string
	err        string
}

type slashCommandDispatcher struct {
	skills *skills.Index
}

func newSlashCommandDispatcher(skillIdx *skills.Index) slashCommandDispatcher {
	if skillIdx == nil {
		skillIdx = skills.Empty()
	}
	return slashCommandDispatcher{skills: skillIdx}
}

// Dispatch processes a `/`-prefixed input as a REPL command. Built-in
// commands win on name collision with skills.
func (d slashCommandDispatcher) Dispatch(text string) slashCommandResult {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return slashCommandResult{kind: slashCommandUnknown, err: "unknown command:  (try /help)"}
	}
	cmd := fields[0]
	args := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	switch cmd {
	case "/new", "/clear":
		return slashCommandResult{
			kind:    slashCommandReset,
			notices: []string{"started a new conversation"},
		}
	case "/help":
		return slashCommandResult{
			kind:    slashCommandHelp,
			notices: d.helpNotices(),
		}
	}
	if name := strings.TrimPrefix(cmd, "/"); name != "" {
		if _, ok := d.skills.Get(name); ok {
			return slashCommandResult{
				kind:       slashCommandStartTurn,
				startInput: skillInvocationMessage(name, args),
			}
		}
	}
	return slashCommandResult{
		kind: slashCommandUnknown,
		err:  "unknown command: " + cmd + " (try /help)",
	}
}

func (d slashCommandDispatcher) helpNotices() []string {
	notices := []string{"commands: /new (or /clear) to reset · /help · Ctrl+C to cancel turn or quit · Alt+Enter (or Shift+Enter with /terminal-setup) for newline · PgUp/PgDn to scroll"}
	if list := d.skills.List(); len(list) > 0 {
		var b strings.Builder
		b.WriteString("skills (invoke with /<name> [args]):")
		for _, s := range list {
			b.WriteString("\n  /")
			b.WriteString(s.Name)
			b.WriteString(" — ")
			b.WriteString(s.Description)
		}
		notices = append(notices, b.String())
	}
	return notices
}

// skillInvocationMessage formats a user-typed `/skill args` into the message
// the model receives. The wrapping mirrors the convention Claude Code uses so
// skills authored for that harness behave the same here.
func skillInvocationMessage(name, args string) string {
	var b strings.Builder
	b.WriteString("<command-name>/")
	b.WriteString(name)
	b.WriteString("</command-name>\n<command-args>")
	b.WriteString(args)
	b.WriteString("</command-args>\n\nRun the ")
	b.WriteString(name)
	b.WriteString(" skill (load it with the skill tool) and follow its instructions, using the arguments above.")
	return b.String()
}
