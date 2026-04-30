package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"lambda/internal/agent"
)

// --- messages / events plumbing ---

type agentEventMsg struct{ ev agent.Event }
type turnEndedMsg struct{} // event channel closed; ensures turnActive always clears
type askMsg struct{ req *confirmRequest }

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

func (m *uiModel) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-m.eventCh
		if !ok {
			return turnEndedMsg{}
		}
		return agentEventMsg{ev: ev}
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
	switch e := ev.(type) {
	case agent.EventThinkingDelta:
		if n := len(m.blocks); n == 0 || m.blocks[n-1].kind != blockThinking || m.blocks[n-1].final {
			m.blocks = append(m.blocks, block{kind: blockThinking})
		}
		m.blocks[len(m.blocks)-1].text += e.Text
	case agent.EventContentDelta:
		m.finalizeOpenThinking()
		if n := len(m.blocks); n == 0 || m.blocks[n-1].kind != blockAssistant || m.blocks[n-1].final {
			m.blocks = append(m.blocks, block{kind: blockAssistant})
		}
		m.blocks[len(m.blocks)-1].text += e.Text
	case agent.EventAssistantDone:
		m.finalizeOpenThinking()
		if n := len(m.blocks); n == 0 || m.blocks[n-1].kind != blockAssistant || m.blocks[n-1].final {
			m.blocks = append(m.blocks, block{kind: blockAssistant, text: e.Text})
		} else {
			m.blocks[len(m.blocks)-1].text = e.Text
		}
		m.blocks[len(m.blocks)-1].final = true
	case agent.EventToolStart:
		m.stepsUsed++
		summary := m.renderToolCall(e.Name, e.Args)
		m.blocks = append(m.blocks, block{kind: blockTool, tool: e.Name, text: summary, final: false})
	case agent.EventToolResult:
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockTool && !m.blocks[n-1].final {
			m.blocks[n-1].text += "\n" + indentLines(clipResult(e.Result), "    ")
			m.blocks[n-1].final = true
		}
	case agent.EventToolDenied:
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("denied %s", e.Name), final: true})
	case agent.EventTurnDone:
		m.finalizeOpenThinking()
		m.turnActive = false
		if m.turnCancel != nil {
			m.turnCancel()
		}
		m.input.Focus()
		if e.Reason != "done" {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: e.Reason, final: true})
		}
	case agent.EventError:
		m.finalizeOpenThinking()
		m.turnActive = false
		if m.turnCancel != nil {
			m.turnCancel()
		}
		m.input.Focus()
		m.blocks = append(m.blocks, block{kind: blockError, text: e.Err.Error(), final: true})
	}
	m.refreshViewport()
}

// finalizeOpenThinking marks the trailing thinking block (if any) final, so
// further content/tool blocks render below it and renderBlock collapses it
// into a "(thought for N words)" stub.
func (m *uiModel) finalizeOpenThinking() {
	if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockThinking && !m.blocks[n-1].final {
		m.blocks[n-1].final = true
	}
}
