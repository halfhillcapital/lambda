package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, m.handleWindowSize(msg)

	case spinner.TickMsg:
		return m, m.handleSpinnerTick(msg)

	case askMsg:
		return m, m.handleAsk(msg)

	case agentEventMsg:
		return m, m.handleAgentEventMsg(msg)

	case quitSummaryMsg:
		return m, m.handleQuitSummary(msg)

	case mergePreviewMsg:
		return m, m.handleMergePreview(msg)

	case mergeResultMsg:
		return m, m.handleMergeResult(msg)

	case turnEndedMsg:
		return m, m.handleTurnEnded(msg)

	case tea.KeyMsg:
		return m, m.handleKey(msg)
	}

	return m, m.updateInputAndViewport(msg)
}

func (m *uiModel) handleWindowSize(msg tea.WindowSizeMsg) tea.Cmd {
	m.width, m.height = msg.Width, msg.Height
	_ = m.rebuildRenderer(msg.Width)
	m.layout()
	m.refreshViewport()
	return nil
}

func (m *uiModel) handleSpinnerTick(msg spinner.TickMsg) tea.Cmd {
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return cmd
}

func (m *uiModel) handleAsk(msg askMsg) tea.Cmd {
	m.approval.Open(msg.req)
	m.input.Blur()
	return m.waitForAsk()
}

func (m *uiModel) handleAgentEventMsg(msg agentEventMsg) tea.Cmd {
	if !m.turn.Current(msg.turn) {
		return nil // stale waiter from a previous turn
	}
	m.handleEvent(msg.ev)
	return m.turn.Wait()
}

func (m *uiModel) handleQuitSummary(msg quitSummaryMsg) tea.Cmd {
	if !msg.hasChanges {
		return tea.Quit
	}
	m.quit.Open(msg.body)
	return nil
}

func (m *uiModel) handleTurnEnded(msg turnEndedMsg) tea.Cmd {
	// Catch-all for turn exit without a TurnDone/Error event (ctx cancel
	// drops in-flight emits); TurnDone/Error already finishes the runner.
	if !m.turn.Current(msg.turn) {
		return nil // stale close from a previous turn
	}
	if !m.turn.Active() {
		return nil
	}
	m.transcript.FinalizeOpenThinking()
	m.turn.Finish()
	m.input.Focus()
	m.transcript.AppendNotice("cancelled")
	m.refreshViewport()
	return nil
}

func (m *uiModel) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.approval.Active() {
		if m.approval.HandleKey(msg.String()) {
			m.input.Focus()
		}
		return nil
	}
	if m.quit.Active() {
		return m.handleQuitKey(msg)
	}
	if m.merge.Active() {
		return m.handleMergeKey(msg)
	}
	return m.handleGlobalKey(msg)
}

func (m *uiModel) handleGlobalKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return m.handleCtrlC()
	case "enter":
		return m.handleEnter()
	case "alt+enter", "ctrl+j":
		m.input.InsertString("\n")
		m.adjustInputHeight()
		return nil
	case "pgup":
		m.viewport.ScrollUp(m.viewport.Height / 2)
		return nil
	case "pgdown":
		m.viewport.ScrollDown(m.viewport.Height / 2)
		return nil
	default:
		return m.updateInputAndViewport(msg)
	}
}

func (m *uiModel) handleCtrlC() tea.Cmd {
	if m.turn.Active() {
		m.turn.Cancel()
		return nil
	}
	if m.session == nil {
		return tea.Quit
	}
	m.input.Blur()
	return m.summarizeForQuit()
}

func (m *uiModel) handleEnter() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" || m.turn.Active() {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		return m.handleSlashCommand(text)
	}
	return m.startTurn(text)
}

func (m *uiModel) updateInputAndViewport(msg tea.Msg) tea.Cmd {
	var tcmd, vcmd tea.Cmd
	if mouseMsg, ok := msg.(tea.MouseMsg); ok {
		switch mouseMsg.Type {
		case tea.MouseWheelUp, tea.MouseWheelDown:
			// Route wheel scrolling exclusively to the transcript viewport.
			m.viewport, vcmd = m.viewport.Update(msg)
			return vcmd
		}
	}
	m.input, tcmd = m.input.Update(msg)
	m.viewport, vcmd = m.viewport.Update(msg)
	m.adjustInputHeight()
	return tea.Batch(tcmd, vcmd)
}

// handleSlashCommand processes a `/`-prefixed input as a REPL command.
// Built-in commands win on name collision with skills. A `/<skill>` matching
// a loaded skill starts a turn whose user message asks the model to run the
// skill (the model loads the body via the `skill` tool). Unknown commands
// surface an error block; no model call is made.
//
// Returns a tea.Cmd when the dispatch starts a model turn (skill invocation),
// otherwise nil.
func (m *uiModel) handleSlashCommand(text string) tea.Cmd {
	m.input.Reset()
	m.adjustInputHeight()
	result := m.commands.Dispatch(text)
	switch result.kind {
	case slashCommandReset:
		if m.rebuildSections != nil {
			m.sections = m.rebuildSections()
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
		for _, notice := range result.notices {
			m.transcript.AppendNotice(notice)
		}
		m.refreshViewport()
		return nil
	case slashCommandHelp:
		for _, notice := range result.notices {
			m.transcript.AppendNotice(notice)
		}
		m.refreshViewport()
		return nil
	case slashCommandShowContext:
		m.showContext()
		return nil
	case slashCommandShowWorktree:
		m.showWorktree()
		return nil
	case slashCommandMerge:
		return m.startMerge()
	case slashCommandSuspend:
		return m.suspend()
	case slashCommandListSessions:
		m.listSessions()
		return nil
	case slashCommandStartTurn:
		return m.startTurn(result.startInput)
	}
	m.transcript.AppendError(result.err)
	m.refreshViewport()
	return nil
}

// summarizeForQuit asks the worktree session for a change summary off the UI
// thread and posts a quitSummaryMsg with the result. Update handles the
// message: no changes → quit immediately; changes → open the quit modal.
func (m *uiModel) summarizeForQuit() tea.Cmd {
	ws := m.session.Workspace()
	return func() tea.Msg {
		body, hasChanges := ws.Summary(context.Background())
		return quitSummaryMsg{body: body, hasChanges: hasChanges}
	}
}

func (m *uiModel) handleQuitKey(msg tea.KeyMsg) tea.Cmd {
	switch result := m.quit.HandleKey(msg.String()); result {
	case quitDialogKeep, quitDialogDiscard:
		m.chosenAction = result.WorktreeAction()
		return tea.Quit
	case quitDialogCancel:
		m.input.Focus()
		return nil
	default:
		return nil
	}
}

func (m *uiModel) startTurn(userInput string) tea.Cmd {
	m.input.Reset()
	m.adjustInputHeight()
	m.transcript.AppendUser(userInput)
	m.refreshViewport()

	m.stepsUsed = 0
	m.turnCost = 0
	return m.turn.Start(userInput)
}
