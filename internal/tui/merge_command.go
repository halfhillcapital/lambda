package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"lambda/internal/worktree"
)

// mergePreviewMsg is posted off-thread once MergePreview completes. err is
// non-nil when the session can't be merged (disabled, detached HEAD, dirty
// parent repo, parent on the wrong branch).
type mergePreviewMsg struct {
	preview worktree.MergePreview
	err     error
}

// mergeResultMsg is posted off-thread once Merge completes.
type mergeResultMsg struct {
	result worktree.MergeResult
	err    error
}

// startMerge kicks off /merge: refuses if a turn is in flight, otherwise
// computes the preview off-thread and posts mergePreviewMsg. The modal opens
// when the message arrives — keeping the precondition checks (which run git)
// off the UI goroutine.
func (m *uiModel) startMerge() tea.Cmd {
	if m.turn.Active() {
		m.transcript.AppendError("/merge: cancel the current turn first (Ctrl+C), then retry")
		m.refreshViewport()
		return nil
	}
	ws := m.session.Workspace()
	return func() tea.Msg {
		preview, err := ws.MergePreview(context.Background())
		return mergePreviewMsg{preview: preview, err: err}
	}
}

func (m *uiModel) handleMergePreview(msg mergePreviewMsg) tea.Cmd {
	if msg.err != nil {
		m.transcript.AppendError("/merge: " + mergeErrorMessage(msg.err))
		m.refreshViewport()
		return nil
	}
	m.merge.Open(msg.preview)
	m.input.Blur()
	return nil
}

func (m *uiModel) handleMergeKey(msg tea.KeyMsg) tea.Cmd {
	switch m.merge.HandleKey(msg.String()) {
	case mergeDialogConfirm:
		preview := m.merge.preview
		ws := m.session.Workspace()
		subject := preview.Subject
		return func() tea.Msg {
			result, err := ws.Merge(context.Background(), subject)
			return mergeResultMsg{result: result, err: err}
		}
	case mergeDialogCancel:
		m.input.Focus()
		return nil
	default:
		return nil
	}
}

func (m *uiModel) handleMergeResult(msg mergeResultMsg) tea.Cmd {
	m.input.Focus()
	if msg.err != nil {
		m.transcript.AppendError("/merge: " + mergeErrorMessage(msg.err))
		m.refreshViewport()
		return nil
	}
	// Same conversation reset as /clear, so the next task starts clean.
	if m.builders.Sections != nil && m.session != nil {
		m.sections = m.builders.Sections(m.session)
		m.agent.ResetWithSystemPrompt(m.sections.Joined())
	} else {
		m.agent.Reset()
	}
	if m.session != nil {
		m.session.History().RecordReset()
	}
	m.transcript.Reset()
	m.turnCost = 0
	m.sessionCost = 0
	m.transcript.AppendNotice(mergeSuccessNotice(msg.result))
	m.refreshViewport()
	return nil
}

// mergeErrorMessage turns a worktree merge error into a one-line user notice.
// Sentinel errors get a hint about how to fix the precondition.
func mergeErrorMessage(err error) string {
	switch {
	case errors.Is(err, worktree.ErrMergeDisabled):
		return "no active session worktree (lambda is editing the current checkout directly)"
	case errors.Is(err, worktree.ErrMergeDetached):
		return "session was started from a detached HEAD; no branch to merge into. Use git merge manually."
	case errors.Is(err, worktree.ErrMergeParentDirty):
		return "parent repo has uncommitted changes; commit or stash them, then retry"
	case errors.Is(err, worktree.ErrMergeParentBranch):
		return err.Error() + "; check it out first, then retry"
	default:
		return err.Error()
	}
}

func mergeSuccessNotice(r worktree.MergeResult) string {
	if r.NoOp {
		return "no changes to merge — fresh worktree on " + r.NewBranch
	}
	subj := r.Subject
	if subj == "" {
		subj = "(no subject)"
	}
	short := r.CommitSHA
	if len(short) > 12 {
		short = short[:12]
	}
	return "merged " + r.OldBranch + " → " + short + " (" + subj + "); fresh worktree on " + r.NewBranch
}
