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
	"github.com/charmbracelet/lipgloss"

	"lambda/internal/agent"
	"lambda/internal/config"
	"lambda/internal/prompt"
	"lambda/internal/skills"
	"lambda/internal/tools"
	"lambda/internal/worktree"
)

// --- model ---

type uiModel struct {
	cfg      *config.Config
	agent    *agent.Agent
	session  *worktree.Session
	registry tools.Registry
	askCh    chan confirmRequest
	// rebuildSections re-renders the system prompt from scratch as discrete
	// chunks, picking up edits to AGENTS.md / CLAUDE.md and a fresh git
	// status. Called on /new and /clear (joined back into a string and fed
	// to the agent), at which point the result is cached in `sections`.
	rebuildSections func() prompt.Sections
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

func newUIModel(cfg *config.Config, sections prompt.Sections, rebuildSections func() prompt.Sections, registry tools.Registry, skillIdx *skills.Index, session *worktree.Session) (*uiModel, error) {
	if skillIdx == nil {
		skillIdx = skills.Empty()
	}
	m := &uiModel{
		cfg:             cfg,
		session:         session,
		registry:        registry,
		askCh:           make(chan confirmRequest, 1),
		commands:        newSlashCommandDispatcher(skillIdx),
		rebuildSections: rebuildSections,
		sections:        sections,
	}
	systemPrompt := sections.Joined()
	m.transcript = newTranscript(func(name, rawArgs string) string {
		return registry.Summarize(name, rawArgs)
	})
	m.approval = newApprovalDialog(func(name, args string) []tools.PreviewLine {
		return registry.Preview(name, args)
	})
	m.quit = &quitDialog{}
	m.merge = &mergeDialog{}
	logger, logErr := agent.OpenDebugLog(cfg)
	approver := agent.NewApprover(registry, m.confirmer, cfg.Yolo)
	m.agent = agent.New(cfg, systemPrompt, registry, approver, logger)
	m.turn = newTurnRunner(m.agent.Run)
	m.tokenUsed, m.tokenCap = m.agent.ContextUsage()
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
func Run(ctx context.Context, cfg *config.Config, sections prompt.Sections, rebuildSections func() prompt.Sections, registry tools.Registry, skillIdx *skills.Index, session *worktree.Session) (worktree.Action, error) {
	m, err := newUIModel(cfg, sections, rebuildSections, registry, skillIdx, session)
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
