package tui

import (
	"fmt"
	"math"
	"strings"

	"lambda/internal/agent"
	"lambda/internal/prompt"
	"lambda/internal/tools"
)

// showContext renders the /context output and appends it as transcript
// notices. Two notices: the breakdown table and a verbatim system-prompt
// dump. Sections are re-derived on the fly so the breakdown reflects the
// current AGENTS.md / CLAUDE.md and live git status.
func (m *uiModel) showContext() {
	var sections prompt.Sections
	if m.rebuildSections != nil {
		sections = m.rebuildSections()
	}
	breakdown, dump := renderContextCommand(m.agent.ContextSnapshot(), sections, m.registry)
	m.transcript.AppendNotice(breakdown)
	m.transcript.AppendNotice(dump)
	m.refreshViewport()
}

// renderContextCommand assembles the two notices the /context command emits:
// a token-by-token breakdown of the current context window, and a verbatim
// dump of the rebuilt system prompt. Section sizes for the system prompt are
// re-derived from BuildSections at call time, which can drift slightly from
// what's literally in the agent's history (env block carries live git
// status); the discrepancy is tolerable for a debug command.
func renderContextCommand(snap agent.ContextSnapshot, sections prompt.Sections, registry tools.Registry) (breakdown, dump string) {
	toolChars := registry.SchemaChars()
	toolCount := len(registry)

	baseTok := charsToTokens(len(sections.Base), snap.CharsPerToken)
	envTok := charsToTokens(len(sections.Environment), snap.CharsPerToken)
	projTok := charsToTokens(len(sections.Project), snap.CharsPerToken)
	skillsTok := charsToTokens(len(sections.Skills), snap.CharsPerToken)
	toolsTok := charsToTokens(toolChars, snap.CharsPerToken)
	userTok := charsToTokens(snap.UserChars, snap.CharsPerToken)
	asstTok := charsToTokens(snap.AssistantChars, snap.CharsPerToken)
	toolMsgTok := charsToTokens(snap.ToolChars, snap.CharsPerToken)
	noteTok := charsToTokens(snap.ElisionNoteChars, snap.CharsPerToken)
	total := baseTok + envTok + projTok + skillsTok + toolsTok + userTok + asstTok + toolMsgTok + noteTok

	calLabel := "default"
	if snap.Calibrated {
		calLabel = "calibrated"
	}

	var b strings.Builder
	if snap.MaxContextTokens > 0 {
		pct := 100.0 * float64(total) / float64(snap.MaxContextTokens)
		fmt.Fprintf(&b, "context: %s / %s tokens (%.0f%%) · ratio %.2f chars/tok (%s)\n\n",
			formatTokens(total), formatTokens(snap.MaxContextTokens), pct, snap.CharsPerToken, calLabel)
	} else {
		fmt.Fprintf(&b, "context: %s tokens (uncapped) · ratio %.2f chars/tok (%s)\n\n",
			formatTokens(total), snap.CharsPerToken, calLabel)
	}

	rows := []ctxRow{
		{indent: "  ", label: "system prompt"},
		{indent: "    ", label: "base instructions", tokens: baseTok},
		{indent: "    ", label: "environment", tokens: envTok},
		{indent: "    ", label: "project context", tokens: projTok, note: projectNote(sections.Project)},
		{indent: "    ", label: "skills listing", tokens: skillsTok, note: countNote("skills", skillsCount(sections.Skills))},
		{indent: "  ", label: "tool schemas", tokens: toolsTok, note: countNote("tools", toolCount)},
		{indent: "  ", label: "messages"},
		{indent: "    ", label: "user", tokens: userTok, note: countNote("msgs", snap.UserMsgs)},
		{indent: "    ", label: "assistant", tokens: asstTok, note: countNote("msgs", snap.AssistantMsgs)},
		{indent: "    ", label: "tool", tokens: toolMsgTok, note: countNote("msgs", snap.ToolMsgs)},
		{indent: "  ", label: "elision note", tokens: noteTok},
		{indent: "  ", label: "total", tokens: total, isTotal: true},
	}
	formatRows(&b, rows)
	breakdown = strings.TrimRight(b.String(), "\n")

	dump = "--- system prompt ---\n" + sections.Joined() + "\n--- end ---"
	return breakdown, dump
}

type ctxRow struct {
	indent  string
	label   string
	tokens  int
	note    string
	isTotal bool
}

// formatRows lays out rows with a token column right-aligned to the longest
// numeric value, and inserts a dashed separator before the total row.
func formatRows(b *strings.Builder, rows []ctxRow) {
	const labelCol = 26
	tokenWidth := 1
	for _, r := range rows {
		if w := len(formatTokens(r.tokens)); w > tokenWidth {
			tokenWidth = w
		}
	}
	for _, r := range rows {
		if r.isTotal {
			fmt.Fprintf(b, "  %s\n", strings.Repeat("─", labelCol+tokenWidth))
		}
		label := r.indent + r.label
		pad := labelCol - len(label)
		if pad < 1 {
			pad = 1
		}
		// Header rows (no token value, no note) print label only.
		if r.tokens == 0 && r.note == "" && (r.label == "system prompt" || r.label == "messages") {
			fmt.Fprintf(b, "%s\n", label)
			continue
		}
		fmt.Fprintf(b, "%s%s%*s", label, strings.Repeat(" ", pad), tokenWidth, formatTokens(r.tokens))
		if r.note != "" {
			fmt.Fprintf(b, "   %s", r.note)
		}
		b.WriteString("\n")
	}
}

func charsToTokens(chars int, ratio float64) int {
	if chars <= 0 {
		return 0
	}
	if ratio <= 0 {
		ratio = 3.5
	}
	return int(math.Ceil(float64(chars) / ratio))
}

func formatTokens(n int) string {
	// Comma-separated thousands, no abbreviation — debug view wants the
	// real numbers.
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}

func projectNote(block string) string {
	// The project block opens with "## Project instructions" then a line
	// naming the source file. Surface the filename when present.
	if block == "" {
		return ""
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if strings.Contains(block, name) {
			return "(" + name + ")"
		}
	}
	return ""
}

func skillsCount(block string) int {
	if block == "" {
		return 0
	}
	n := 0
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "- ") {
			n++
		}
	}
	return n
}

func countNote(unit string, n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("(%d %s)", n, unit)
}
