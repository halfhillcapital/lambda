package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"lambda/internal/tools"
)

func renderModal(req *confirmRequest, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ToolCallStyle.Render("Approve call to "+req.name+"?"))
	b.WriteString(modalPreview(req.name, req.args))
	b.WriteString("\n\n")
	b.WriteString(statusStyle.Render("[y] once   [a] always this tool   [A] always all   [n/Esc] deny"))
	maxw := width - 6
	if maxw < 30 {
		maxw = 30
	}
	return modalBoxStyle.Width(maxw).Render(b.String())
}

func renderQuitModal(body string, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ToolCallStyle.Render("Session left changes — keep or discard?"))
	b.WriteString(toolOutStyle.Render(body))
	b.WriteString("\n\n")
	b.WriteString(statusStyle.Render("[k] keep on branch   [d] discard   [Esc] back to chat"))
	maxw := width - 6
	if maxw < 30 {
		maxw = 30
	}
	return modalBoxStyle.Width(maxw).Render(b.String())
}

func modalPreview(name, rawArgs string) string {
	switch name {
	case tools.Bash.Name():
		a, _ := tools.Bash.Decode(rawArgs)
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Render("$ " + a.Command)
	case tools.Write.Name():
		a, _ := tools.Write.Decode(rawArgs)
		lines := strings.Split(a.Content, "\n")
		preview := strings.Join(firstN(lines, 20), "\n")
		head := toolOutStyle.Render(fmt.Sprintf("%s — %d bytes, %d lines", a.Path, len(a.Content), len(lines)))
		if len(lines) > 20 {
			preview += toolOutStyle.Render(fmt.Sprintf("\n… (%d more lines)", len(lines)-20))
		}
		return head + "\n" + toolOutStyle.Render(preview)
	case tools.Edit.Name():
		a, _ := tools.Edit.Decode(rawArgs)
		return toolOutStyle.Render(a.Path+":") + "\n" + renderDiff(a.OldString, a.NewString)
	}
	return toolOutStyle.Render(Truncate(rawArgs, 400))
}

func renderDiff(oldS, newS string) string {
	var b strings.Builder
	for _, l := range strings.Split(oldS, "\n") {
		b.WriteString(diffDelStyle.Render("- "+l) + "\n")
	}
	for _, l := range strings.Split(newS, "\n") {
		b.WriteString(diffAddStyle.Render("+ "+l) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstN[T any](s []T, n int) []T {
	if len(s) < n {
		return s
	}
	return s[:n]
}
