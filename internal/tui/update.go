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
		if msg.turn != m.turn {
			return m, nil // stale waiter from a previous turn
		}
		m.handleEvent(msg.ev)
		cmds = append(cmds, waitForEvent(m.eventCh, m.turn))

	case quitSummaryMsg:
		if !msg.hasChanges {
			return m, tea.Quit
		}
		m.quitModal = quitModalState{active: true, body: msg.body}
		return m, nil

	case turnEndedMsg:
		// Catch-all for turn exit without a TurnDone/Error event (ctx cancel
		// drops in-flight emits); TurnDone/Error already cleared turnActive.
		if msg.turn != m.turn {
			return m, nil // stale close from a previous turn
		}
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
			if m.session == nil {
				return m, tea.Quit
			}
			m.input.Blur()
			return m, m.summarizeForQuit()
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.turnActive {
				return m, nil
			}
			if strings.HasPrefix(text, "/") {
				return m, m.handleSlashCommand(text)
			}
			return m, m.startTurn(text)
		case "alt+enter", "ctrl+j":
			m.input.InsertString("\n")
			m.adjustInputHeight()
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
	if mouseMsg, ok := msg.(tea.MouseMsg); ok {
		switch mouseMsg.Type {
		case tea.MouseWheelUp, tea.MouseWheelDown:
			// Route wheel scrolling exclusively to the transcript viewport.
			m.viewport, vcmd = m.viewport.Update(msg)
			return m, vcmd
		}
	}
	m.input, tcmd = m.input.Update(msg)
	m.viewport, vcmd = m.viewport.Update(msg)
	m.adjustInputHeight()
	cmds = append(cmds, tcmd, vcmd)
	return m, tea.Batch(cmds...)
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
	fields := strings.Fields(text)
	cmd := fields[0]
	args := strings.TrimSpace(strings.TrimPrefix(text, cmd))
	switch cmd {
	case "/new", "/clear":
		m.agent.Reset()
		m.blocks = nil
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "started a new conversation", final: true})
		m.refreshViewport()
		return nil
	case "/help":
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "commands: /new (or /clear) to reset · /help · Ctrl+C to cancel turn or quit · Alt+Enter (or Shift+Enter with /terminal-setup) for newline · PgUp/PgDn to scroll", final: true})
		if list := m.skills.List(); len(list) > 0 {
			var b strings.Builder
			b.WriteString("skills (invoke with /<name> [args]):")
			for _, s := range list {
				b.WriteString("\n  /")
				b.WriteString(s.Name)
				b.WriteString(" — ")
				b.WriteString(s.Description)
			}
			m.blocks = append(m.blocks, block{kind: blockNotice, text: b.String(), final: true})
		}
		m.refreshViewport()
		return nil
	}
	if name := strings.TrimPrefix(cmd, "/"); name != "" {
		if _, ok := m.skills.Get(name); ok {
			return m.startTurn(skillInvocationMessage(name, args))
		}
	}
	m.blocks = append(m.blocks, block{kind: blockError, text: "unknown command: " + cmd + " (try /help)", final: true})
	m.refreshViewport()
	return nil
}

// skillInvocationMessage formats a user-typed `/skill args` into the message
// the model receives. The wrapping mirrors the convention Claude Code uses so
// skills authored for that harness behave the same here.
func skillInvocationMessage(name, args string) string {
	var b strings.Builder
	b.WriteString("<command-name>/")
	b.WriteString(name)
	b.WriteString("</command-name>\n<command-args>")
	b.WriteString(args)
	b.WriteString("</command-args>\n\nRun the ")
	b.WriteString(name)
	b.WriteString(" skill (load it with the skill tool) and follow its instructions, using the arguments above.")
	return b.String()
}

// summarizeForQuit asks the worktree session for a change summary off the UI
// thread and posts a quitSummaryMsg with the result. Update handles the
// message: no changes → quit immediately; changes → open the quit modal.
func (m *uiModel) summarizeForQuit() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		body, hasChanges := session.Summary(context.Background())
		return quitSummaryMsg{body: body, hasChanges: hasChanges}
	}
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
	m.adjustInputHeight()
	m.blocks = append(m.blocks, block{kind: blockUser, text: userInput, final: true})
	m.refreshViewport()

	m.turnCtx, m.turnCancel = context.WithCancel(context.Background())
	m.turnActive = true
	m.stepsUsed = 0
	m.turn++

	// Replace eventCh per turn: Run closes it when done. Stale waiters
	// holding the previous channel will simply observe its close and
	// produce a turnEndedMsg tagged with the old turn id, which Update
	// drops.
	m.eventCh = make(chan agent.Event, 128)
	go m.agent.Run(m.turnCtx, userInput, m.eventCh)
	return waitForEvent(m.eventCh, m.turn)
}
