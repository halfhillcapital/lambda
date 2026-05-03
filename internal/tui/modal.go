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

// mergeDialog is the confirmation modal shown by /merge before any commit
// lands on the user's base branch. It is purely presentational; the merge
// itself is driven by handleMergeKey in update.go.
type mergeDialog struct {
	active  bool
	preview worktree.MergePreview
}

type mergeDialogResult int

const (
	mergeDialogNoop mergeDialogResult = iota
	mergeDialogConfirm
	mergeDialogCancel
)

func (d *mergeDialog) Active() bool {
	return d != nil && d.active
}

func (d *mergeDialog) Open(preview worktree.MergePreview) {
	d.active = true
	d.preview = preview
}

func (d *mergeDialog) HandleKey(key string) mergeDialogResult {
	switch key {
	case "y", "Y", "enter":
		d.active = false
		return mergeDialogConfirm
	case "n", "N", "esc", "ctrl+c":
		d.active = false
		return mergeDialogCancel
	}
	return mergeDialogNoop
}

func (d *mergeDialog) Render(width int) string {
	p := d.preview
	var b strings.Builder
	if p.NoOp {
		fmt.Fprintf(&b, "%s\n\n", ToolCallStyle.Render("No changes to merge — start a fresh session?"))
	} else {
		fmt.Fprintf(&b, "%s\n\n", ToolCallStyle.Render("Squash-merge session onto "+p.BaseBranch+"?"))
	}
	var body strings.Builder
	fmt.Fprintf(&body, "base:    %s @ %s\n", p.BaseBranch, p.BaseShortSHA)
	fmt.Fprintf(&body, "branch:  %s\n", p.SessionBranch)
	if !p.NoOp {
		fmt.Fprintf(&body, "subject: %s", p.Subject)
		if p.DiffStat != "" {
			body.WriteString("\n")
			body.WriteString(p.DiffStat)
		}
	}
	b.WriteString(toolOutStyle.Render(body.String()))
	b.WriteString("\n\n")
	if p.NoOp {
		b.WriteString(statusStyle.Render("[y/Enter] rotate worktree   [n/Esc] cancel"))
	} else {
		b.WriteString(statusStyle.Render("[y/Enter] merge & rotate   [n/Esc] cancel"))
	}
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
