package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"

	"lambda/internal/session"
)

// discard handles `/discard [<prefix>]`. With a prefix that resolves
// to a non-current session, it tears that session down and surfaces a
// notice. With no prefix (or a prefix matching the current session),
// it tears the current session down and quits — the caller can
// `lambda` again to start fresh, or `lambda --resume` another one.
func (m *uiModel) discard(arg string) tea.Cmd {
	if m.turn.Active() {
		m.transcript.AppendError("/discard: cancel the current turn first (Ctrl+C), then retry")
		m.refreshViewport()
		return nil
	}
	if m.session == nil {
		m.transcript.AppendError("/discard: no session attached")
		m.refreshViewport()
		return nil
	}

	if arg == "" {
		return m.discardCurrent()
	}

	repoRoot := m.session.RepoRoot()
	if repoRoot == "" {
		m.transcript.AppendError("/discard: not inside a git repo — no persisted sessions to discard")
		m.refreshViewport()
		return nil
	}
	target, err := session.Resolve(repoRoot, arg)
	if err != nil {
		m.transcript.AppendError("/discard: " + err.Error())
		m.refreshViewport()
		return nil
	}
	if target.ID == m.session.ID() {
		return m.discardCurrent()
	}
	if err := session.Discard(context.Background(), repoRoot, target.ID); err != nil {
		m.transcript.AppendError("/discard: " + discardErrorMessage(err))
		m.refreshViewport()
		return nil
	}
	m.transcript.AppendNotice("discarded session " + target.ID)
	m.refreshViewport()
	return nil
}

// discardCurrent tears the current session's workspace, branch, and
// manifest down, then quits. main.go's deferred Finalize is a no-op
// because Session.Discard sets the suspended flag.
func (m *uiModel) discardCurrent() tea.Cmd {
	if err := m.session.Discard(context.Background()); err != nil {
		m.transcript.AppendError("/discard: " + discardErrorMessage(err))
		m.refreshViewport()
		return nil
	}
	return tea.Quit
}

func discardErrorMessage(err error) string {
	if errors.Is(err, session.ErrLockHeld) {
		return err.Error() + " — suspend that lambda first, or wait for it to exit"
	}
	return err.Error()
}
