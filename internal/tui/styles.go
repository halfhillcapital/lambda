package tui

import "github.com/charmbracelet/lipgloss"

// Styles exported for reuse by the non-interactive (one-shot) runner.
var (
	ErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f")).Bold(true)
	ToolCallStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffaf5f"))
)

var (
	userStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#87d7ff")).
			Bold(true).
			Border(lipgloss.ThickBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("#87d7ff")).
			PaddingLeft(1).
			MarginTop(1)
	assistantStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))
	thinkingHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262")).Italic(true)
	thinkingBodyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	toolOutStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8a8a"))
	toolBlockStyle      = lipgloss.NewStyle().MarginTop(1)
	noticeStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#d7d787")).Italic(true)
	statusStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#626262"))
	warnStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd75f"))
	modalBoxStyle       = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#ffaf5f")).
				Padding(0, 1)
	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false).
			BorderForeground(lipgloss.Color("#3a3a3a"))
	diffAddStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#87ff87"))
	diffDelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff5f5f"))
)
