package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"lambda/internal/tools"
)

// --- transcript blocks ---

type blockKind int

const (
	blockUser blockKind = iota
	blockAssistant
	blockThinking
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
		return userStyle.Render(b.text)
	case blockAssistant:
		if b.final && m.renderer != nil {
			if out, err := m.renderer.Render(b.text); err == nil {
				return assistantStyle.Render(strings.TrimRight(out, "\n"))
			}
		}
		return assistantStyle.Render(b.text)
	case blockThinking:
		body := strings.TrimSpace(b.text)
		if b.final {
			words := len(strings.Fields(body))
			if words == 0 {
				return ""
			}
			return thinkingHeaderStyle.Render(fmt.Sprintf("(thought for %d words)", words))
		}
		header := thinkingHeaderStyle.Render("thinking…")
		if body == "" {
			return header
		}
		return header + "\n" + thinkingBodyStyle.Render(indentLines(body, "  "))
	case blockTool:
		return toolBlockStyle.Render(b.text)
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
	case tools.ReadFile:
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
