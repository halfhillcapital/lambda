package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"lambda/internal/agent"
	"lambda/internal/worktree"
)

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		_ = m.rebuildRenderer(msg.Width)
		m.layout()
		m.refreshViewport()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case askMsg:
		m.pendingAsk = msg.req
		m.input.Blur()
		return m, m.waitForAsk()

	case agentEventMsg:
		m.handleEvent(msg.ev)
		cmds = append(cmds, m.waitForEvent())

	case turnEndedMsg:
		// Catch-all for turn exit without a TurnDone/Error event (ctx cancel
		// drops in-flight emits); TurnDone/Error already cleared turnActive.
		if m.turnActive {
			m.finalizeOpenThinking()
			m.turnActive = false
			if m.turnCancel != nil {
				m.turnCancel()
			}
			m.input.Focus()
			m.blocks = append(m.blocks, block{kind: blockNotice, text: "cancelled", final: true})
			m.refreshViewport()
		}
		return m, nil

	case tea.KeyMsg:
		if m.pendingAsk != nil {
			return m, m.handleConfirmKey(msg)
		}
		if m.quitModal.active {
			return m, m.handleQuitKey(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			if m.turnActive {
				m.turnCancel()
				return m, nil
			}
			if m.tryOpenQuitModal() {
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.turnActive {
				return m, nil
			}
			if strings.HasPrefix(text, "/") {
				m.handleSlashCommand(text)
				return m, nil
			}
			return m, m.startTurn(text)
		case "ctrl+j":
			m.input.InsertString("\n")
			return m, nil
		case "pgup":
			m.viewport.ScrollUp(m.viewport.Height / 2)
			return m, nil
		case "pgdown":
			m.viewport.ScrollDown(m.viewport.Height / 2)
			return m, nil
		}
	}

	var tcmd, vcmd tea.Cmd
	m.input, tcmd = m.input.Update(msg)
	m.viewport, vcmd = m.viewport.Update(msg)
	cmds = append(cmds, tcmd, vcmd)
	return m, tea.Batch(cmds...)
}

// handleSlashCommand processes a `/`-prefixed input as a REPL command.
// Unknown commands surface an error block; recognised commands act and
// append a notice. No model call is made.
func (m *uiModel) handleSlashCommand(text string) {
	m.input.Reset()
	cmd := strings.Fields(text)[0]
	switch cmd {
	case "/new", "/clear":
		m.agent.Reset()
		m.blocks = nil
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "started a new conversation", final: true})
	case "/help":
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "commands: /new (or /clear) to reset · /help · Ctrl+C to cancel turn or quit · Ctrl+J for newline · PgUp/PgDn to scroll", final: true})
	default:
		m.blocks = append(m.blocks, block{kind: blockError, text: "unknown command: " + cmd + " (try /help)", final: true})
	}
	m.refreshViewport()
}

// tryOpenQuitModal returns true and opens the quit modal if the worktree
// session has changes worth deciding on; otherwise returns false so the
// caller can quit immediately.
func (m *uiModel) tryOpenQuitModal() bool {
	if m.session == nil {
		return false
	}
	body, hasChanges := m.session.Summary(context.Background())
	if !hasChanges {
		return false
	}
	m.quitModal = quitModalState{active: true, body: body}
	m.input.Blur()
	return true
}

func (m *uiModel) handleQuitKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "k", "K":
		m.chosenAction = worktree.ActionKeep
		m.quitModal.active = false
		return tea.Quit
	case "d", "D":
		m.chosenAction = worktree.ActionDiscard
		m.quitModal.active = false
		return tea.Quit
	case "esc", "ctrl+c":
		m.quitModal.active = false
		m.input.Focus()
		return nil
	}
	return nil
}

func (m *uiModel) handleConfirmKey(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	var d agent.Decision
	switch k {
	case "y", "Y", "enter":
		d = agent.DecisionAllow
	case "a":
		d = agent.DecisionAlwaysTool
	case "A":
		d = agent.DecisionAlwaysAll
	case "n", "N", "esc", "ctrl+c":
		d = agent.DecisionDeny
	default:
		return nil
	}
	m.pendingAsk.reply <- d
	m.pendingAsk = nil
	m.input.Focus()
	return nil
}

func (m *uiModel) startTurn(userInput string) tea.Cmd {
	m.input.Reset()
	m.blocks = append(m.blocks, block{kind: blockUser, text: userInput, final: true})
	m.refreshViewport()

	m.turnCtx, m.turnCancel = context.WithCancel(context.Background())
	m.turnActive = true
	m.stepsUsed = 0
	m.errMsg = ""

	// Replace eventCh per turn: Run closes it when done.
	m.eventCh = make(chan agent.Event, 128)
	go m.agent.Run(m.turnCtx, userInput, m.eventCh)
	return m.waitForEvent()
}
