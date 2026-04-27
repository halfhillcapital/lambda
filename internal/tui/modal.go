package tui

import (
	"encoding/json"
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
	var m map[string]any
	_ = json.Unmarshal([]byte(rawArgs), &m)
	switch tools.Name(name) {
	case tools.Bash:
		cmd, _ := m["command"].(string)
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Render("$ " + cmd)
	case tools.WriteFile:
		p, _ := m["path"].(string)
		c, _ := m["content"].(string)
		lines := strings.Split(c, "\n")
		preview := strings.Join(firstN(lines, 20), "\n")
		head := toolOutStyle.Render(fmt.Sprintf("%s — %d bytes, %d lines", p, len(c), len(lines)))
		if len(lines) > 20 {
			preview += toolOutStyle.Render(fmt.Sprintf("\n… (%d more lines)", len(lines)-20))
		}
		return head + "\n" + toolOutStyle.Render(preview)
	case tools.EditFile:
		p, _ := m["path"].(string)
		oldS, _ := m["old_string"].(string)
		newS, _ := m["new_string"].(string)
		return toolOutStyle.Render(p+":") + "\n" + renderDiff(oldS, newS)
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
