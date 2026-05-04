// Package tui implements the Bubble Tea REPL for lambda.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"lambda/internal/agent"
	"lambda/internal/ai"
	"lambda/internal/config"
	"lambda/internal/prompt"
	"lambda/internal/session"
	"lambda/internal/skills"
	"lambda/internal/tools"
	"lambda/internal/worktree"
)

// Builders bundles the constructors the TUI needs to (re)build per-session
// state: sections (system prompt), registry (tool set), and a fresh
// Session itself. They get re-invoked on /new so a successor Session
// gets its own freshly-rooted registry and prompt.
type Builders struct {
	Sections   func(*session.Session) prompt.Sections
	Registry   func(*session.Session) tools.Registry
	NewSession func(context.Context) (*session.Session, error)
}

// --- model ---

type uiModel struct {
	cfg      *config.Config
	agent    *agent.Agent
	session  *session.Session
	registry tools.Registry
	skillIdx *skills.Index
	logger   *agent.Logger
	approver *agent.Approver
	builders Builders
	askCh    chan confirmRequest
	// sections is the cached system-prompt breakdown matching the prompt
	// the agent currently has in history[0]. Used by /context so the
	// breakdown reflects what was actually sent rather than reshelling out
	// to `git status` / `uname` on every invocation.
	sections prompt.Sections

	commands     slashCommandDispatcher
	turn         *turnRunner
	stepsUsed    int
	tokenUsed    int
	tokenCap     int
	turnCost     float64 // USD spent in the current round, reset on startTurn
	sessionCost  float64 // running total across the session

	viewport   viewport.Model
	input      textarea.Model
	spinner    spinner.Model
	transcript *transcript

	approval *approvalDialog
	quit     *quitDialog
	merge    *mergeDialog
	// chosenAction is read by the caller after Run returns to decide
	// whether to keep or discard the session worktree. Defaults to
	// ActionKeep (zero value) when the user never opens the modal.
	chosenAction worktree.Action

	width, height int
	// inputRows is the number of visual rows the input box currently shows.
	// The textarea's own height is pinned to maxInputRows so its internal
	// viewport never has to scroll when the cursor crosses a wrap; we crop
	// the rendered output to inputRows in footer().
	inputRows int
}

func newUIModel(cfg *config.Config, builders Builders, skillIdx *skills.Index, sess *session.Session) (*uiModel, error) {
	if skillIdx == nil {
		skillIdx = skills.Empty()
	}
	m := &uiModel{
		cfg:      cfg,
		session:  sess,
		skillIdx: skillIdx,
		builders: builders,
		askCh:    make(chan confirmRequest, 1),
		commands: newSlashCommandDispatcher(skillIdx),
		sections: builders.Sections(sess),
		registry: builders.Registry(sess),
	}
	m.transcript = newTranscript(func(name, rawArgs string) string {
		return m.registry.Summarize(name, rawArgs)
	})
	m.approval = newApprovalDialog(func(name, args string) []tools.PreviewLine {
		return m.registry.Preview(name, args)
	})
	m.quit = &quitDialog{}
	m.merge = &mergeDialog{}
	logger, logErr := agent.OpenDebugLog(cfg)
	m.logger = logger
	m.attachAgent(m.sections.Joined())
	if sess != nil {
		replay, err := sess.History().Load()
		if err != nil {
			m.transcript.AppendNotice("history replay disabled: " + err.Error())
		} else if len(replay) > 0 {
			m.agent.LoadReplay(replay)
			m.replayTranscript(replay)
			m.tokenUsed, m.tokenCap = m.agent.ContextUsage()
		}
	}
	if m.tokenCap == 0 {
		m.tokenUsed, m.tokenCap = m.agent.ContextUsage()
	}
	if logErr != nil {
		// Stderr is hidden by the alt-screen, so surface this in the UI.
		m.transcript.AppendNotice("log file disabled: " + logErr.Error())
	}

	ta := textarea.New()
	ta.Placeholder = "Ask anything — Enter to send, Alt+Enter for newline, /help for commands, Ctrl+C to quit"
	ta.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return "> "
		}
		return "  "
	})
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline.SetEnabled(false)
	ta.SetHeight(maxInputRows)
	ta.Focus()
	m.input = ta
	m.inputRows = 1

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
	return m.transcript.RebuildRenderer(width)
}

// attachAgent (re)builds the Agent and its turn runner against the
// currently-attached registry and session. Shared logger is reused
// across agents — the TUI owns its lifecycle separately. Called from
// newUIModel and from /new (after the session/registry swap).
func (m *uiModel) attachAgent(systemPrompt string) {
	m.approver = agent.NewApprover(m.registry, m.confirmer, m.cfg.Yolo)
	m.agent = agent.New(m.cfg, systemPrompt, m.registry, m.approver, m.logger)
	m.turn = newTurnRunner(m.agent.Run)
}

// swapSession is the /new path: persist+release the current session,
// allocate a fresh one via the configured factory, swap it in
// in-process, and rebuild everything that's anchored to a session
// (registry, sections, system prompt, agent). On factory failure the
// current session stays attached and an error is surfaced.
func (m *uiModel) swapSession(ctx context.Context) error {
	if m.builders.NewSession == nil {
		return fmt.Errorf("/new is not available in this mode")
	}
	newSess, err := m.builders.NewSession(ctx)
	if err != nil {
		return err
	}
	if m.session != nil {
		if err := m.session.Suspend(ctx); err != nil {
			// Don't abort: the new session is already constructed and
			// holding the lock. Surface the suspend error as a notice
			// rather than rolling back, which would mean discarding the
			// fresh session we just paid to create.
			m.transcript.AppendNotice("note: prior session suspend failed: " + err.Error())
		}
	}
	m.session = newSess
	if ws := newSess.Workspace(); ws != nil && ws.Enabled {
		if err := os.Chdir(ws.Path); err != nil {
			return fmt.Errorf("chdir to new workspace: %w", err)
		}
	}
	m.registry = m.builders.Registry(newSess)
	m.sections = m.builders.Sections(newSess)
	m.attachAgent(m.sections.Joined())

	m.transcript.Reset()
	m.turnCost = 0
	m.sessionCost = 0
	m.stepsUsed = 0
	m.tokenUsed, m.tokenCap = m.agent.ContextUsage()
	return nil
}

// replayTranscript rehydrates the visible transcript from a resumed
// session's persisted message log. Mirrors the strip rule the agent
// applies in LoadReplay (only user + assistant text), so the user
// sees what the model will see — pre-resume tool churn is hidden.
func (m *uiModel) replayTranscript(messages []ai.Message) {
	for _, msg := range messages {
		switch msg.Role {
		case ai.RoleUser:
			m.transcript.AppendUser(msg.Content)
		case ai.RoleAssistant:
			if msg.Content == "" {
				continue
			}
			m.transcript.AppendAssistant(msg.Content)
		}
	}
	m.transcript.AppendNotice(fmt.Sprintf("resumed session %s", m.session.ID()))
}

func (m *uiModel) refreshViewport() {
	m.viewport.SetContent(m.transcript.Render())
	m.viewport.GotoBottom()
}

func (m *uiModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
		m.waitForAsk(),
	)
}

func (m *uiModel) View() string {
	if m.width == 0 {
		return "initializing…"
	}

	if m.approval.Active() {
		modal := m.approval.Render(m.width)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	if m.quit.Active() {
		modal := m.quit.Render(m.width)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	if m.merge.Active() {
		modal := m.merge.Render(m.width)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal)
	}

	var b strings.Builder
	b.WriteString(m.viewport.View())
	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

func (m *uiModel) footer() string {
	inputBox := inputBoxStyle.Width(m.width).Render(cropLines(m.input.View(), m.inputRows))

	statusL := statusStyle.Render(fmt.Sprintf("%s @ %s", m.cfg.Model, m.cfg.BaseURL))
	tokens := m.renderTokenUsage()
	sep := statusStyle.Render(" · ")
	var statusR string
	if m.turn.Active() {
		statusR = m.spinner.View() + statusStyle.Render(fmt.Sprintf(" step %d/%d", m.stepsUsed, m.cfg.MaxSteps)) + sep + tokens
		if cost := m.renderSessionCost(); cost != "" {
			statusR += sep + cost
		}
		statusR += sep + statusStyle.Render("Ctrl+C cancel")
	} else {
		statusR = tokens
		if cost := m.renderSessionCost(); cost != "" {
			statusR += sep + cost
		}
		statusR += sep + statusStyle.Render("ready")
	}
	pad := m.width - lipgloss.Width(statusL) - lipgloss.Width(statusR)
	if pad < 1 {
		pad = 1
	}
	status := statusL + strings.Repeat(" ", pad) + statusR
	return inputBox + "\n" + status
}

// renderTokenUsage returns a colored "used/cap" summary (or just "used" when
// the cap is disabled). Yellow at 80%+, red at 95%+ so the user sees the
// context budget getting tight before compaction starts silently trimming.
func (m *uiModel) renderTokenUsage() string {
	used, capacity := m.tokenUsed, m.tokenCap
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

// renderSessionCost returns the running session cost formatted for the status
// line, or "" when no cost has been reported (the common case for local
// servers that don't bill).
func (m *uiModel) renderSessionCost() string {
	if m.sessionCost <= 0 {
		return ""
	}
	return statusStyle.Render(formatCost(m.sessionCost))
}

// formatCost renders a USD amount with enough precision to be useful for
// per-call costs while staying compact: $0.0042, $0.42, $4.20, $42.00.
func formatCost(usd float64) string {
	if usd >= 1 {
		return fmt.Sprintf("$%.2f", usd)
	}
	return fmt.Sprintf("$%.4f", usd)
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
	inputH := m.inputRows
	if inputH < 1 {
		inputH = 1
	}
	// viewport height = total - input - input borders (2) - statusbar - spacing
	vh := m.height - inputH - 4
	if vh < 3 {
		vh = 3
	}
	m.viewport.Width = m.width
	m.viewport.Height = vh
	m.input.SetWidth(m.width)
}

// maxInputRows caps the auto-grow so the input cannot eat the transcript.
const maxInputRows = 10

// adjustInputHeight recomputes the visual (soft-wrapped) row count for the
// current input and re-runs layout when it changes. The textarea's own height
// stays pinned to maxInputRows so its internal viewport never scrolls; we
// crop the rendered output to inputRows in footer().
func (m *uiModel) adjustInputHeight() {
	contentW := m.input.Width()
	if contentW < 1 {
		contentW = 1
	}
	want := 0
	for _, line := range strings.Split(m.input.Value(), "\n") {
		w := lipgloss.Width(line)
		if w == 0 {
			want++
		} else {
			want += (w + contentW - 1) / contentW
		}
	}
	if want < 1 {
		want = 1
	}
	if want > maxInputRows {
		want = maxInputRows
	}
	if want != m.inputRows {
		m.inputRows = want
		m.layout()
	}
}

// cropLines returns the first n lines of s joined by "\n". Used to trim the
// textarea's fixed-height render down to the visual rows we actually want
// to show.
func cropLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}

// Run starts the Bubble Tea REPL. It blocks until the program exits and
// returns the user's keep/discard choice for the worktree session. The
// returned action is ActionKeep when the user never reaches the quit
// modal (e.g. clean session, or worktree disabled).
func Run(ctx context.Context, cfg *config.Config, builders Builders, skillIdx *skills.Index, sess *session.Session) (worktree.Action, error) {
	m, err := newUIModel(cfg, builders, skillIdx, sess)
	if err != nil {
		return worktree.ActionKeep, err
	}
	defer func() {
		// /new swaps in successor agents but never closes their loggers
		// (the model owns the shared logger lifecycle). The current
		// agent's Close runs first so the file handle is released
		// exactly once, here.
		if m.agent != nil {
			_ = m.agent.Close()
		}
	}()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		return worktree.ActionKeep, err
	}
	return m.chosenAction, nil
}
