package agent

import (
	"fmt"
	"slices"

	"github.com/openai/openai-go"
)

// compactIfNeeded drops oldest turns until the message list fits inside
// maxContextChars, preserving leading system messages and at least the most
// recent turn. A single system note records how many turns were elided.
// If the last remaining turn alone exceeds the budget, we fall back to
// truncating its largest tool message bodies so the request still fits.
func (a *Agent) compactIfNeeded() {
	if a.maxContextChars <= 0 {
		return
	}
	for a.totalChars() > a.maxContextChars {
		headEnd := a.skipSystemHead()
		from := a.firstUserAfter(headEnd)
		if from < 0 {
			break
		}
		to := a.firstUserAfter(from + 1)
		if to < 0 {
			break // only one turn left; drop-loop can't help
		}
		a.messages = slices.Delete(a.messages, from, to)
		a.droppedTurns++
		// The note (if any) was inside the leading-system run, so its index
		// doesn't shift when we delete from `from` (which is past headEnd).
	}
	if a.totalChars() > a.maxContextChars {
		a.shrinkLargestToolMessages()
	}
	a.refreshElisionNote()
}

// shrinkLargestToolMessages truncates tool message bodies, largest first, until
// the total fits the budget or nothing meaningful is left to shrink. Called
// only after the drop loop has exhausted droppable turns.
func (a *Agent) shrinkLargestToolMessages() {
	const minBody = 512
	for a.totalChars() > a.maxContextChars {
		idx, size := a.largestToolMessage()
		if idx < 0 || size <= minBody {
			return
		}
		m := a.messages[idx].OfTool
		body := m.Content.OfString.Value
		target := max(size/2, minBody)
		trimmed := body[:target] + fmt.Sprintf("\n… (tool result truncated from %d to %d bytes to fit context)", len(body), target)
		a.messages[idx] = openai.ToolMessage(trimmed, m.ToolCallID)
	}
}

// largestToolMessage returns the index and string-content length of the
// biggest tool message in the history, or (-1, 0) if none has string content.
func (a *Agent) largestToolMessage() (int, int) {
	bestIdx, bestSize := -1, 0
	for i, m := range a.messages {
		if m.OfTool == nil || !m.OfTool.Content.OfString.Valid() {
			continue
		}
		s := m.OfTool.Content.OfString.Value
		if len(s) > bestSize {
			bestSize = len(s)
			bestIdx = i
		}
	}
	return bestIdx, bestSize
}

// refreshElisionNote inserts or updates a system message recording how many
// turns were dropped. Inserted right after the last leading system message so
// it joins the "head" and is itself preserved by future compactions.
func (a *Agent) refreshElisionNote() {
	if a.droppedTurns == 0 {
		return
	}
	note := openai.SystemMessage(fmt.Sprintf(
		"[note: %d earlier turn(s) omitted to fit the context window]",
		a.droppedTurns,
	))
	if a.elisionNoteIdx > 0 && a.elisionNoteIdx < len(a.messages) {
		a.messages[a.elisionNoteIdx] = note
		return
	}
	insertAt := a.skipSystemHead()
	a.messages = slices.Insert(a.messages, insertAt, note)
	a.elisionNoteIdx = insertAt
}

// totalChars estimates the size of the message list as the sum of each
// message's JSON-marshalled length. Matches the bytes actually sent to the
// API, so it tracks model context budget directly (modulo tokenization).
func (a *Agent) totalChars() int {
	n := 0
	for _, m := range a.messages {
		b, err := m.MarshalJSON()
		if err != nil {
			continue
		}
		n += len(b)
	}
	return n
}

// skipSystemHead returns the index of the first non-system message.
func (a *Agent) skipSystemHead() int {
	i := 0
	for i < len(a.messages) && roleOf(a.messages[i]) == "system" {
		i++
	}
	return i
}

// firstUserAfter returns the index of the first user message at or after start,
// or -1 if none.
func (a *Agent) firstUserAfter(start int) int {
	for i := start; i < len(a.messages); i++ {
		if roleOf(a.messages[i]) == "user" {
			return i
		}
	}
	return -1
}

// roleOf returns the message's role string ("system" / "user" / "assistant" /
// "tool" / "developer" / "function"), or "" if undetermined. We discriminate
// by the union's OfX fields rather than GetRole because the typed-constant
// role fields (e.g. constant.User) zero-value to "" and GetRole returns that.
func roleOf(m openai.ChatCompletionMessageParamUnion) string {
	switch {
	case m.OfSystem != nil:
		return "system"
	case m.OfUser != nil:
		return "user"
	case m.OfAssistant != nil:
		return "assistant"
	case m.OfTool != nil:
		return "tool"
	case m.OfDeveloper != nil:
		return "developer"
	case m.OfFunction != nil:
		return "function"
	}
	return ""
}
