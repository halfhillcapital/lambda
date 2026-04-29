// Package tui implements the Bubble Tea REPL for lambda.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"lambda/internal/agent"
	"lambda/internal/config"
	"lambda/internal/tools"
	"lambda/internal/worktree"
)

// --- model ---

type uiModel struct {
	cfg     *config.Config
	agent   *agent.Agent
	session *worktree.Session
	askCh   chan confirmRequest
	eventCh chan agent.Event

	turnCtx    context.Context
	turnCancel context.CancelFunc
	turnActive bool
	stepsUsed  int

	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model
	renderer *glamour.TermRenderer

	blocks []block

	pendingAsk *confirmRequest
	quitModal  quitModalState
	// chosenAction is read by the caller after Run returns to decide
	// whether to keep or discard the session worktree. Defaults to
	// ActionKeep (zero value) when the user never opens the modal.
	chosenAction worktree.Action

	width, height int
	errMsg        string
}

type quitModalState struct {
	active bool
	body   string
}

func newUIModel(cfg *config.Config, systemPrompt string, pol agent.Policy, session *worktree.Session) (*uiModel, error) {
	m := &uiModel{
		cfg:     cfg,
		session: session,
		askCh:   make(chan confirmRequest, 1),
		eventCh: make(chan agent.Event, 128),
	}
	logger, logErr := agent.OpenDebugLog(cfg)
	approver := agent.NewApprover(pol, m.confirmer, cfg.Yolo)
	m.agent = agent.New(cfg, systemPrompt, tools.Default, approver, logger)
	if logErr != nil {
		// Stderr is hidden by the alt-screen, so surface this in the UI.
		m.blocks = append(m.blocks, block{kind: blockNotice, text: "log file disabled: " + logErr.Error(), final: true})
	}

	ta := textarea.New()
	ta.Placeholder = "Ask anything — Enter to send, Ctrl+J for newline, /help for commands, Ctrl+C to quit"
	ta.Prompt = "│ "
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline.SetEnabled(false)
	ta.SetHeight(3)
	ta.Focus()
	m.input = ta

	vp := viewport.New(80, 20)
	vp.SetContent("")
	vp.KeyMap = viewport.KeyMap{}
	m.viewport = vp

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaf5f"))
	m.spinner = sp

	if err := m.rebuildRenderer(80); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *uiModel) rebuildRenderer(width int) error {
	if width < 20 {
		width = 20
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width-4),
	)
	if err != nil {
		return err
	}
	m.renderer = r
	return nil
}

func (m *uiModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
		m.waitForAsk(),
		m.waitForEvent(),
	)
}

func (m *uiModel) View() string {
	if m.width == 0 {
		return "initializing…"
	}

	if m.pendingAsk != nil {
		modal := renderModal(m.pendingAsk, m.width)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	if m.quitModal.active {
		modal := renderQuitModal(m.quitModal.body, m.width)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m *uiModel) footer() string {
	statusL := statusStyle.Render(fmt.Sprintf("%s @ %s", m.cfg.Model, m.cfg.BaseURL))
	tokens := m.renderTokenUsage()
	sep := statusStyle.Render(" · ")
	var statusR string
	if m.turnActive {
		statusR = m.spinner.View() + statusStyle.Render(fmt.Sprintf(" step %d/%d", m.stepsUsed, m.cfg.MaxSteps)) + sep + tokens + sep + statusStyle.Render("Ctrl+C cancel")
	} else {
		statusR = tokens + sep + statusStyle.Render("ready")
	}
	pad := m.width - lipgloss.Width(statusL) - lipgloss.Width(statusR)
	if pad < 1 {
		pad = 1
	}
	status := statusL + strings.Repeat(" ", pad) + statusR
	return status + "\n" + m.input.View()
}

// renderTokenUsage returns a colored "used/cap" summary (or just "used" when
// the cap is disabled). Yellow at 80%+, red at 95%+ so the user sees the
// context budget getting tight before compaction starts silently trimming.
func (m *uiModel) renderTokenUsage() string {
	used, capacity := m.agent.ContextUsage()
	if capacity <= 0 {
		return statusStyle.Render(formatTokenCount(used) + " tok")
	}
	label := formatTokenCount(used) + "/" + formatTokenCount(capacity) + " tok"
	ratio := float64(used) / float64(capacity)
	switch {
	case ratio >= 0.95:
		return ErrorStyle.Render(label)
	case ratio >= 0.80:
		return warnStyle.Render(label)
	default:
		return statusStyle.Render(label)
	}
}

// formatTokenCount renders n as either a raw number (under 1K) or a "12.3k"
// abbreviation (1K+). Keeps the footer compact on narrow terminals.
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func (m *uiModel) layout() {
	inputH := m.input.Height()
	if inputH < 3 {
		inputH = 3
	}
	// viewport height = total - input - statusbar - spacing
	vh := m.height - inputH - 2
	if vh < 3 {
		vh = 3
	}
	m.viewport.Width = m.width
	m.viewport.Height = vh
	m.input.SetWidth(m.width)
}

// Run starts the Bubble Tea REPL. It blocks until the program exits and
// returns the user's keep/discard choice for the worktree session. The
// returned action is ActionKeep when the user never reaches the quit
// modal (e.g. clean session, or worktree disabled).
func Run(ctx context.Context, cfg *config.Config, systemPrompt string, pol agent.Policy, session *worktree.Session) (worktree.Action, error) {
	m, err := newUIModel(cfg, systemPrompt, pol, session)
	if err != nil {
		return worktree.ActionKeep, err
	}
	defer m.agent.Close()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		return worktree.ActionKeep, err
	}
	return m.chosenAction, nil
}
