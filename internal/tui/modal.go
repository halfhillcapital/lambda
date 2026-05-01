package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"lambda/internal/agent"
	"lambda/internal/tools"
	"lambda/internal/worktree"
)

type approvalDialog struct {
	pending *confirmRequest
	preview func(name, args string) []tools.PreviewLine
}

func newApprovalDialog(preview func(name, args string) []tools.PreviewLine) *approvalDialog {
	return &approvalDialog{preview: preview}
}

func (d *approvalDialog) Active() bool {
	return d != nil && d.pending != nil
}

func (d *approvalDialog) Open(req *confirmRequest) {
	d.pending = req
}

func (d *approvalDialog) HandleKey(key string) bool {
	decision, ok := approvalDecisionForKey(key)
	if !ok || d.pending == nil {
		return false
	}
	d.pending.reply <- decision
	d.pending = nil
	return true
}

func approvalDecisionForKey(key string) (agent.Decision, bool) {
	switch key {
	case "y", "Y", "enter":
		return agent.DecisionAllow, true
	case "a":
		return agent.DecisionAlwaysTool, true
	case "A":
		return agent.DecisionAlwaysAll, true
	case "n", "N", "esc", "ctrl+c":
		return agent.DecisionDeny, true
	default:
		return agent.DecisionDeny, false
	}
}

func (d *approvalDialog) Render(width int) string {
	req := d.pending
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ToolCallStyle.Render("Approve call to "+req.name+"?"))
	if d.preview != nil {
		b.WriteString(renderToolPreview(d.preview(req.name, req.args)))
	}
	b.WriteString("\n\n")
	b.WriteString(statusStyle.Render("[y] once   [a] always this tool   [A] always all   [n/Esc] deny"))
	maxw := width - 6
	if maxw < 30 {
		maxw = 30
	}
	return modalBoxStyle.Width(maxw).Render(b.String())
}

type quitDialog struct {
	active bool
	body   string
}

type quitDialogResult int

const (
	quitDialogNoop quitDialogResult = iota
	quitDialogKeep
	quitDialogDiscard
	quitDialogCancel
)

func (d *quitDialog) Active() bool {
	return d != nil && d.active
}

func (d *quitDialog) Open(body string) {
	d.active = true
	d.body = body
}

func (d *quitDialog) HandleKey(key string) quitDialogResult {
	result := quitResultForKey(key)
	if result == quitDialogNoop {
		return quitDialogNoop
	}
	d.active = false
	return result
}

func quitResultForKey(key string) quitDialogResult {
	switch key {
	case "k", "K":
		return quitDialogKeep
	case "d", "D":
		return quitDialogDiscard
	case "esc", "ctrl+c":
		return quitDialogCancel
	default:
		return quitDialogNoop
	}
}

func (r quitDialogResult) WorktreeAction() worktree.Action {
	switch r {
	case quitDialogDiscard:
		return worktree.ActionDiscard
	default:
		return worktree.ActionKeep
	}
}

func (d *quitDialog) Render(width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ToolCallStyle.Render("Session left changes — keep or discard?"))
	b.WriteString(toolOutStyle.Render(d.body))
	b.WriteString("\n\n")
	b.WriteString(statusStyle.Render("[k] keep on branch   [d] discard   [Esc] back to chat"))
	maxw := width - 6
	if maxw < 30 {
		maxw = 30
	}
	return modalBoxStyle.Width(maxw).Render(b.String())
}

func renderToolPreview(lines []tools.PreviewLine) string {
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		switch line.Kind {
		case tools.PreviewCommand:
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Render(line.Text))
		case tools.PreviewRemoved:
			b.WriteString(diffDelStyle.Render(line.Text))
		case tools.PreviewAdded:
			b.WriteString(diffAddStyle.Render(line.Text))
		default:
			b.WriteString(toolOutStyle.Render(line.Text))
		}
	}
	return b.String()
}
