package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"

	"lambda/internal/agent"
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

type transcript struct {
	blocks    []block
	renderer  *glamour.TermRenderer
	summarize func(name, rawArgs string) string
}

type transcriptEventResult struct {
	toolStarted    bool
	turnDone       bool
	turnDoneReason string
	tokenUsed      int
	tokenCap       int
	hasTokenUsage  bool
	turnCost       float64 // per-completion cost, accumulated by the caller
	sessionCost    float64 // running session total reported by the agent
	hasCost        bool
}

func newTranscript(summarize func(name, rawArgs string) string) *transcript {
	return &transcript{summarize: summarize}
}

func (t *transcript) RebuildRenderer(width int) error {
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
	t.renderer = r
	return nil
}

func (t *transcript) Reset() {
	t.blocks = nil
}

func (t *transcript) AppendUser(text string) {
	t.blocks = append(t.blocks, block{kind: blockUser, text: text, final: true})
}

func (t *transcript) AppendNotice(text string) {
	t.blocks = append(t.blocks, block{kind: blockNotice, text: text, final: true})
}

func (t *transcript) AppendError(text string) {
	t.blocks = append(t.blocks, block{kind: blockError, text: text, final: true})
}

func (t *transcript) Render() string {
	var sb strings.Builder
	for i, bl := range t.blocks {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(t.renderBlock(bl))
	}
	return sb.String()
}

func (t *transcript) ApplyAgentEvent(ev agent.Event) transcriptEventResult {
	var result transcriptEventResult
	switch e := ev.(type) {
	case agent.EventThinkingDelta:
		if n := len(t.blocks); n == 0 || t.blocks[n-1].kind != blockThinking || t.blocks[n-1].final {
			t.blocks = append(t.blocks, block{kind: blockThinking})
		}
		t.blocks[len(t.blocks)-1].text += e.Text
	case agent.EventContentDelta:
		t.FinalizeOpenThinking()
		if n := len(t.blocks); n == 0 || t.blocks[n-1].kind != blockAssistant || t.blocks[n-1].final {
			t.blocks = append(t.blocks, block{kind: blockAssistant})
		}
		t.blocks[len(t.blocks)-1].text += e.Text
	case agent.EventAssistantDone:
		t.FinalizeOpenThinking()
		if n := len(t.blocks); n == 0 || t.blocks[n-1].kind != blockAssistant || t.blocks[n-1].final {
			t.blocks = append(t.blocks, block{kind: blockAssistant, text: e.Text})
		} else {
			t.blocks[len(t.blocks)-1].text = e.Text
		}
		t.blocks[len(t.blocks)-1].final = true
	case agent.EventToolStart:
		t.FinalizeOpenThinking()
		result.toolStarted = true
		summary := t.renderToolCall(e.Name, e.Args)
		t.blocks = append(t.blocks, block{kind: blockTool, tool: e.Name, text: summary, final: false})
	case agent.EventToolResult:
		if n := len(t.blocks); n > 0 && t.blocks[n-1].kind == blockTool && !t.blocks[n-1].final {
			t.blocks[n-1].text += "\n" + indentLines(clipResult(e.Result), "    ")
			t.blocks[n-1].final = true
		}
	case agent.EventToolDenied:
		t.AppendNotice(fmt.Sprintf("denied %s", e.Name))
	case agent.EventContextUsage:
		result.tokenUsed, result.tokenCap = e.Used, e.Limit
		result.hasTokenUsage = true
	case agent.EventCost:
		result.turnCost = e.Turn
		result.sessionCost = e.Session
		result.hasCost = true
	case agent.EventTurnDone:
		t.FinalizeOpenThinking()
		result.turnDone = true
		result.turnDoneReason = e.Reason
	case agent.EventError:
		t.FinalizeOpenThinking()
		result.turnDone = true
		t.AppendError(e.Err.Error())
	}
	return result
}

// FinalizeOpenThinking marks the trailing thinking block (if any) final, so
// further content/tool blocks render below it and renderBlock collapses it
// into a "(thought for N words)" stub.
func (t *transcript) FinalizeOpenThinking() {
	if n := len(t.blocks); n > 0 && t.blocks[n-1].kind == blockThinking && !t.blocks[n-1].final {
		t.blocks[n-1].final = true
	}
}

func (t *transcript) renderBlock(b block) string {
	switch b.kind {
	case blockUser:
		return userStyle.Render(b.text)
	case blockAssistant:
		if b.final && t.renderer != nil {
			if out, err := t.renderer.Render(b.text); err == nil {
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

func (t *transcript) renderToolCall(name, rawArgs string) string {
	summary := rawArgs
	if t.summarize != nil {
		summary = t.summarize(name, rawArgs)
	}
	return ToolCallStyle.Render("→ "+name) + " " + toolOutStyle.Render(summary)
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
