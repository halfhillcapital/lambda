// Package tui implements the Bubble Tea REPL for lambda.
package tui

import (
	"context"
	"encoding/json"
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
)

// Styles exported for reuse by the non-interactive (one-shot) runner.
var (
	ErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f")).Bold(true)
	ToolCallStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaf5f"))
)

var (
	userStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#87d7ff")).Bold(true)
	assistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	toolOutStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	noticeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#d7d787")).Italic(true)
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	warnStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd75f"))
	modalBoxStyle  = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#ffaf5f")).
			Padding(0, 1)
	diffAddStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#87ff87"))
	diffDelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f"))
)

// --- messages / events plumbing ---

type agentEventMsg struct{ ev agent.Event }
type turnEndedMsg struct{} // event channel closed; ensures turnActive always clears
type askMsg struct{ req *confirmRequest }

type confirmRequest struct {
	name, args string
	reply      chan agent.Decision
}

// --- transcript blocks ---

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockTool
	blockNotice
	blockError
)

type block struct {
	kind  blockKind
	text  string // accumulating text (assistant streaming, or full text for others)
	final bool   // assistant: true once the model's message is complete
	tool  string // tool name (blockTool)
}

// --- model ---

type uiModel struct {
	cfg     *config.Config
	agent   *agent.Agent
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

	width, height int
	errMsg        string
}

func newUIModel(cfg *config.Config, systemPrompt string) (*uiModel, error) {
	m := &uiModel{
		cfg:     cfg,
		askCh:   make(chan confirmRequest, 1),
		eventCh: make(chan agent.Event, 128),
	}
	confirmer := func(ctx context.Context, name, args string) agent.Decision {
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
	m.agent = agent.New(cfg, systemPrompt, confirmer)

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
		switch msg.String() {
		case "ctrl+c":
			if m.turnActive {
				m.turnCancel()
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

func (m *uiModel) handleEvent(ev agent.Event) {
	switch e := ev.(type) {
	case agent.EventContentDelta:
		if n := len(m.blocks); n == 0 || m.blocks[n-1].kind != blockAssistant || m.blocks[n-1].final {
			m.blocks = append(m.blocks, block{kind: blockAssistant})
		}
		m.blocks[len(m.blocks)-1].text += e.Text
	case agent.EventAssistantDone:
		if n := len(m.blocks); n == 0 || m.blocks[n-1].kind != blockAssistant || m.blocks[n-1].final {
			m.blocks = append(m.blocks, block{kind: blockAssistant, text: e.Text})
		} else {
			m.blocks[len(m.blocks)-1].text = e.Text
		}
		m.blocks[len(m.blocks)-1].final = true
	case agent.EventToolStart:
		m.stepsUsed++
		summary := renderToolCall(e.Name, e.Args)
		m.blocks = append(m.blocks, block{kind: blockTool, tool: e.Name, text: summary, final: false})
	case agent.EventToolResult:
		if n := len(m.blocks); n > 0 && m.blocks[n-1].kind == blockTool && !m.blocks[n-1].final {
			m.blocks[n-1].text += "\n" + indentLines(clipResult(e.Result), "    ")
			m.blocks[n-1].final = true
		}
	case agent.EventToolDenied:
		m.blocks = append(m.blocks, block{kind: blockNotice, text: fmt.Sprintf("denied %s", e.Name), final: true})
	case agent.EventTurnDone:
		m.turnActive = false
		if m.turnCancel != nil {
			m.turnCancel()
		}
		m.input.Focus()
		if e.Reason != "done" {
			m.blocks = append(m.blocks, block{kind: blockNotice, text: e.Reason, final: true})
		}
	case agent.EventError:
		m.turnActive = false
		if m.turnCancel != nil {
			m.turnCancel()
		}
		m.input.Focus()
		m.blocks = append(m.blocks, block{kind: blockError, text: e.Err.Error(), final: true})
	}
	m.refreshViewport()
}

func (m *uiModel) View() string {
	if m.width == 0 {
		return "initializing…"
	}

	if m.pendingAsk != nil {
		modal := renderModal(m.pendingAsk, m.width)
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

func (m *uiModel) refreshViewport() {
	var sb strings.Builder
	for i, bl := range m.blocks {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(m.renderBlock(bl))
	}
	m.viewport.SetContent(sb.String())
	m.viewport.GotoBottom()
}

func (m *uiModel) renderBlock(b block) string {
	switch b.kind {
	case blockUser:
		return userStyle.Render("› ") + b.text
	case blockAssistant:
		if b.final && m.renderer != nil {
			if out, err := m.renderer.Render(b.text); err == nil {
				return strings.TrimRight(out, "\n")
			}
		}
		return assistantStyle.Render(b.text)
	case blockTool:
		return b.text
	case blockNotice:
		return noticeStyle.Render("· " + b.text)
	case blockError:
		return ErrorStyle.Render("✗ " + b.text)
	}
	return ""
}

// --- tool call rendering ---

func renderToolCall(name, rawArgs string) string {
	head := ToolCallStyle.Render("→ "+name) + " " + toolOutStyle.Render(TerseArgs(name, rawArgs))
	return head
}

// TerseArgs renders a compact, human-readable summary of a tool's JSON arguments.
// Exported for reuse by the non-interactive runner.
func TerseArgs(name, rawArgs string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &m); err != nil {
		return Truncate(rawArgs, 120)
	}
	switch tools.Name(name) {
	case tools.Bash:
		if cmd, ok := m["command"].(string); ok {
			return Truncate(cmd, 240)
		}
	case tools.ReadFile, tools.ListDir:
		if p, ok := m["path"].(string); ok {
			return p
		}
	case tools.WriteFile:
		p, _ := m["path"].(string)
		c, _ := m["content"].(string)
		return fmt.Sprintf("%s (%d bytes)", p, len(c))
	case tools.EditFile:
		p, _ := m["path"].(string)
		return p
	}
	return Truncate(rawArgs, 120)
}

func clipResult(s string) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) <= 10 {
		return toolOutStyle.Render(s)
	}
	head := strings.Join(lines[:10], "\n")
	return toolOutStyle.Render(head + fmt.Sprintf("\n… (%d more lines)", len(lines)-10))
}

func indentLines(s, prefix string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// Truncate trims s to at most n runes (approx), appending an ellipsis if clipped.
// Exported for reuse by the non-interactive runner.
func Truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// --- confirmation modal ---

func renderModal(req *confirmRequest, width int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ToolCallStyle.Render("Approve call to "+req.name+"?"))
	b.WriteString(modalPreview(req.name, req.args))
	b.WriteString("\n\n")
	b.WriteString(statusStyle.Render("[y] once   [a] always this tool   [A] always all   [n/Esc] deny"))
	maxw := width - 6
	if maxw < 30 {
		maxw = 30
	}
	return modalBoxStyle.Width(maxw).Render(b.String())
}

func modalPreview(name, rawArgs string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(rawArgs), &m)
	switch tools.Name(name) {
	case tools.Bash:
		cmd, _ := m["command"].(string)
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Render("$ " + cmd)
	case tools.WriteFile:
		p, _ := m["path"].(string)
		c, _ := m["content"].(string)
		lines := strings.Split(c, "\n")
		preview := strings.Join(firstN(lines, 20), "\n")
		head := toolOutStyle.Render(fmt.Sprintf("%s — %d bytes, %d lines", p, len(c), len(lines)))
		if len(lines) > 20 {
			preview += toolOutStyle.Render(fmt.Sprintf("\n… (%d more lines)", len(lines)-20))
		}
		return head + "\n" + toolOutStyle.Render(preview)
	case tools.EditFile:
		p, _ := m["path"].(string)
		oldS, _ := m["old_string"].(string)
		newS, _ := m["new_string"].(string)
		return toolOutStyle.Render(p+":") + "\n" + renderDiff(oldS, newS)
	}
	return toolOutStyle.Render(Truncate(rawArgs, 400))
}

func renderDiff(oldS, newS string) string {
	var b strings.Builder
	for _, l := range strings.Split(oldS, "\n") {
		b.WriteString(diffDelStyle.Render("- "+l) + "\n")
	}
	for _, l := range strings.Split(newS, "\n") {
		b.WriteString(diffAddStyle.Render("+ "+l) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstN[T any](s []T, n int) []T {
	if len(s) < n {
		return s
	}
	return s[:n]
}

// Run starts the Bubble Tea REPL. It blocks until the program exits.
func Run(ctx context.Context, cfg *config.Config, systemPrompt string) error {
	m, err := newUIModel(cfg, systemPrompt)
	if err != nil {
		return err
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err = p.Run()
	return err
}
