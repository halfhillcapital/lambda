package agent

import (
	"fmt"
	"slices"

	"lambda/internal/ai"
)

// history owns the chat message slice and the bookkeeping for compaction.
// Not safe for concurrent use — Agent serializes per-turn access.
//
// History persistence is *not* this struct's job. The agent emits an
// EventMessageAppended on every append; a surrounding loop (the TUI,
// or anything else driving the agent) is responsible for mirroring
// those to durable storage if it cares.
type history struct {
	messages         []ai.Message
	maxContextTokens int     // soft cap on prompt tokens; <=0 disables compaction
	charsPerToken    float64 // running calibration from server-reported prompt_tokens; 0 means no data yet
	toolSchemaChars  int     // JSON byte size of tool schemas sent on every request; 0 when no tools
	droppedTurns     int
	elisionNoteIdx   int // 0 = no note inserted yet
}

// defaultCharsPerToken is the fallback ratio used until the server reports
// actual prompt_tokens. Chat/tool-call JSON tokenizes denser than plain prose,
// so this is conservative on the low side (over-estimates tokens, triggering
// earlier compaction rather than blowing the context window).
const defaultCharsPerToken = 3.5

// compactStats summarises one compaction pass. The zero value means no-op.
type compactStats struct {
	beforeTokens int
	afterTokens  int
	turnsDropped int
	shrunk       bool
	msgsBefore   int
	msgsAfter    int
}

func (s compactStats) changed() bool { return s.turnsDropped != 0 || s.shrunk }

func newHistory(systemPrompt string, maxContextTokens, toolSchemaChars int) *history {
	return &history{
		messages:         []ai.Message{ai.SystemMessage(systemPrompt)},
		maxContextTokens: maxContextTokens,
		toolSchemaChars:  toolSchemaChars,
	}
}

// append extends the in-memory history. The single entry point for
// adding to history — agent.Run and friends should never poke
// h.messages directly. Persistence (if any) is the surrounding loop's
// concern; the agent emits EventMessageAppended after this call.
func (h *history) append(m ai.Message) {
	h.messages = append(h.messages, m)
}

// reset clears history back to the original system message and forgets any
// compaction state. Used by REPL slash commands like /new.
func (h *history) reset() {
	if len(h.messages) > 0 && roleOf(h.messages[0]) == "system" {
		h.messages = h.messages[:1]
	} else {
		h.messages = h.messages[:0]
	}
	h.droppedTurns = 0
	h.elisionNoteIdx = 0
}

// resetWithSystemPrompt is reset() but with a freshly-rebuilt system prompt.
// Used so /new can pick up edits to project-context files (AGENTS.md, CLAUDE.md).
func (h *history) resetWithSystemPrompt(newPrompt string) {
	h.messages = []ai.Message{ai.SystemMessage(newPrompt)}
	h.droppedTurns = 0
	h.elisionNoteIdx = 0
}

// compactIfNeeded drops oldest turns until the estimated prompt-token count
// fits inside maxContextTokens, preserving leading system messages and at
// least the most recent turn. A single system note records how many turns
// were elided. If the last remaining turn alone exceeds the budget, we fall
// back to truncating its largest tool message bodies so the request still
// fits. Returns stats describing what changed (zero stats == no-op).
func (h *history) compactIfNeeded() compactStats {
	if h.maxContextTokens <= 0 {
		return compactStats{}
	}
	stats := compactStats{
		beforeTokens: h.estimateTokens(),
		msgsBefore:   len(h.messages),
	}
	droppedAtStart := h.droppedTurns
	for h.estimateTokens() > h.maxContextTokens {
		headEnd := h.skipSystemHead()
		from := h.firstUserAfter(headEnd)
		if from < 0 {
			break
		}
		to := h.firstUserAfter(from + 1)
		if to < 0 {
			break // only one turn left; drop-loop can't help
		}
		h.messages = slices.Delete(h.messages, from, to)
		h.droppedTurns++
		// The note (if any) was inside the leading-system run, so its index
		// doesn't shift when we delete from `from` (which is past headEnd).
	}
	stats.shrunk = h.estimateTokens() > h.maxContextTokens
	if stats.shrunk {
		h.shrinkLargestToolMessages()
	}
	h.refreshElisionNote()
	stats.afterTokens = h.estimateTokens()
	stats.turnsDropped = h.droppedTurns - droppedAtStart
	stats.msgsAfter = len(h.messages)
	return stats
}

// shrinkLargestToolMessages truncates tool message bodies, largest first, until
// the total fits the budget or nothing meaningful is left to shrink. Called
// only after the drop loop has exhausted droppable turns. If the per-request
// fixed cost (system prompt + tool schemas) alone exceeds the budget, no
// amount of message-shrinking will help; we exit when an iteration fails to
// reduce the largest tool body, leaving the request over budget.
func (h *history) shrinkLargestToolMessages() {
	const minBody = 512
	for h.estimateTokens() > h.maxContextTokens {
		idx, size := h.largestToolMessage()
		if idx < 0 || size <= minBody {
			return
		}
		m := h.messages[idx]
		body := m.Content
		target := max(size/2, minBody)
		trimmed := body[:target] + fmt.Sprintf("\n… (tool result truncated from %d to %d bytes to fit context)", len(body), target)
		if len(trimmed) >= len(body) {
			// Marker overhead would negate the trim; bail out.
			return
		}
		h.messages[idx] = ai.ToolMessage(trimmed, m.ToolCallID)
	}
}

// largestToolMessage returns the index and string-content length of the
// biggest tool message in the history, or (-1, 0) if none has string content.
func (h *history) largestToolMessage() (int, int) {
	bestIdx, bestSize := -1, 0
	for i, m := range h.messages {
		if m.Role != ai.RoleTool || m.Content == "" {
			continue
		}
		s := m.Content
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
func (h *history) refreshElisionNote() {
	if h.droppedTurns == 0 {
		return
	}
	note := ai.SystemMessage(fmt.Sprintf(
		"[note: %d earlier turn(s) omitted to fit the context window]",
		h.droppedTurns,
	))
	if h.elisionNoteIdx > 0 && h.elisionNoteIdx < len(h.messages) {
		h.messages[h.elisionNoteIdx] = note
		return
	}
	insertAt := h.skipSystemHead()
	h.messages = slices.Insert(h.messages, insertAt, note)
	h.elisionNoteIdx = insertAt
}

// totalChars returns the byte size of everything sent on each request: the
// JSON-marshalled message list plus the tool-schema payload. Tool schemas
// are constant per session but consume real prompt-token budget on the
// server, so they belong in the calibration target alongside messages.
func (h *history) totalChars() int {
	n := h.toolSchemaChars
	for _, m := range h.messages {
		b, err := m.MarshalJSON()
		if err != nil {
			continue
		}
		n += len(b)
	}
	return n
}

// estimateTokens converts totalChars() to a token estimate using the calibration
// ratio from the server's last-reported prompt_tokens, or defaultCharsPerToken
// if we haven't seen a server response yet. Always rounds up (ceiling) so the
// budget check errs on the side of compacting earlier.
func (h *history) estimateTokens() int {
	ratio := h.charsPerToken
	if ratio <= 0 {
		ratio = defaultCharsPerToken
	}
	chars := h.totalChars()
	est := float64(chars) / ratio
	rounded := int(est)
	if est > float64(rounded) {
		rounded++
	}
	return rounded
}

// recordTokenUsage calibrates charsPerToken from the server's prompt_tokens for
// the message set we just sent. Called after each completion; best-effort —
// servers that don't report usage (common for some local inference frameworks)
// leave the calibration at its previous value.
func (h *history) recordTokenUsage(charsSent int, promptTokens int64) {
	if promptTokens <= 0 || charsSent <= 0 {
		return
	}
	h.charsPerToken = float64(charsSent) / float64(promptTokens)
}

// skipSystemHead returns the index of the first non-system message.
func (h *history) skipSystemHead() int {
	i := 0
	for i < len(h.messages) && roleOf(h.messages[i]) == "system" {
		i++
	}
	return i
}

// firstUserAfter returns the index of the first user message at or after start,
// or -1 if none.
func (h *history) firstUserAfter(start int) int {
	for i := start; i < len(h.messages); i++ {
		if roleOf(h.messages[i]) == "user" {
			return i
		}
	}
	return -1
}

// roleOf returns the message's role string ("system" / "user" / "assistant" /
// "tool"), or "" if unset.
func roleOf(m ai.Message) string {
	return string(m.Role)
}
