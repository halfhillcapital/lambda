package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"lambda/internal/agent"
)

// --- messages / events plumbing ---

type askMsg struct{ req *confirmRequest }
type quitSummaryMsg struct {
	body       string
	hasChanges bool
}

type confirmRequest struct {
	name, args string
	reply      chan agent.Decision
}

func (m *uiModel) waitForAsk() tea.Cmd {
	return func() tea.Msg {
		req, ok := <-m.askCh
		if !ok {
			return nil
		}
		return askMsg{req: &req}
	}
}

// confirmer produces an agent.Confirmer that round-trips through the UI's
// ask channel: it posts a confirmRequest the Update loop will pick up, then
// blocks on the reply channel until the user answers (or ctx cancels).
func (m *uiModel) confirmer(ctx context.Context, name, args string) agent.Decision {
	reply := make(chan agent.Decision, 1)
	select {
	case m.askCh <- confirmRequest{name, args, reply}:
	case <-ctx.Done():
		return agent.DecisionDeny
	}
	select {
	case d := <-reply:
		return d
	case <-ctx.Done():
		return agent.DecisionDeny
	}
}

func (m *uiModel) handleEvent(ev agent.Event) {
	result := m.transcript.ApplyAgentEvent(ev)
	if result.toolStarted {
		m.stepsUsed++
	}
	if result.hasTokenUsage {
		m.tokenUsed, m.tokenCap = result.tokenUsed, result.tokenCap
	}
	if result.turnDone {
		m.turn.Finish()
		m.input.Focus()
		if result.turnDoneReason != "" && result.turnDoneReason != "done" {
			m.transcript.AppendNotice(result.turnDoneReason)
		}
	}
	m.refreshViewport()
}
