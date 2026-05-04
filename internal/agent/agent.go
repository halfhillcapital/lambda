// Package agent implements the tool-calling loop that drives a chat model
// through one user turn, emitting events for UI consumers.
package agent

import (
	"context"
	"errors"
	"fmt"

	"lambda/internal/ai"
	"lambda/internal/config"
	"lambda/internal/tools"
)

// Decision is the outcome of a confirmation prompt for a destructive tool call.
type Decision int

const (
	DecisionAllow Decision = iota
	DecisionDeny
	DecisionAlwaysTool
	DecisionAlwaysAll
)

// Confirmer asks the user whether a pending destructive tool call should proceed.
// Implementations block until the user decides.
type Confirmer func(ctx context.Context, name, rawArgs string) Decision

// Event is emitted by the agent as it progresses through a turn.
type Event interface{ isEvent() }

type (
	EventContentDelta  struct{ Text string }
	EventThinkingDelta struct{ Text string }
	EventAssistantDone struct{ Text string }
	EventToolStart     struct{ ID, Name, Args string }
	EventToolResult    struct{ ID, Name, Result string }
	EventToolDenied    struct{ ID, Name string }
	EventContextUsage  struct{ Used, Limit int }
	EventTurnDone      struct{ Reason string }
	EventError         struct{ Err error }
	// EventMessageAppended fires every time a message lands in the
	// in-memory history (user input, assistant reply, tool result,
	// cancellation marker). Surrounding loops persist these to a
	// session log if they care; oneshot mode ignores them. The
	// payload is the exact message the model will see on the next
	// request (pre-compaction).
	EventMessageAppended struct{ Message ai.Message }
	// EventCost reports per-call USD spend and the running session total.
	// Emitted after each completion when the provider reports cost (>0).
	EventCost struct {
		Turn    float64
		Session float64
	}
)

func (EventContentDelta) isEvent()  {}
func (EventThinkingDelta) isEvent() {}
func (EventAssistantDone) isEvent() {}
func (EventToolStart) isEvent()     {}
func (EventToolResult) isEvent()    {}
func (EventToolDenied) isEvent()    {}
func (EventContextUsage) isEvent()  {}
func (EventTurnDone) isEvent()        {}
func (EventError) isEvent()           {}
func (EventCost) isEvent()            {}
func (EventMessageAppended) isEvent() {}

// Agent is the main tool-calling loop. It is not safe for concurrent use —
// one turn at a time per agent.
type Agent struct {
	completer       ai.Completer
	model           string
	registry        tools.Registry
	tools           []ai.ToolSpec
	history         *history
	maxSteps        int
	approver        *Approver
	reasoningEffort ai.ReasoningEffort
	providerCfg     ai.ProviderConfig
	sessionCost     float64

	logger *Logger // nil when --debug is off
}

// New constructs an Agent. registry is the tool registry the agent
// dispatches against (build with tools.New(sessionRoot, skillIdx) for production).
// approver is the single owner of tool-call approval (build it with
// NewApprover, passing the same registry). logger may be nil to disable
// structured logging; pair with OpenDebugLog if --debug is on. The agent
// takes ownership of logger and closes it via Close.
func New(cfg *config.Config, systemPrompt string, registry tools.Registry, approver *Approver, logger *Logger) *Agent {
	a := &Agent{
		completer:       ai.NewOpenAICompleter(cfg.Provider, cfg.BaseURL, cfg.APIKey, !cfg.NoStream),
		model:           cfg.Model,
		registry:        registry,
		tools:           registry.Schemas(),
		history:         newHistory(systemPrompt, cfg.MaxContextTokens, registry.SchemaChars()),
		maxSteps:        cfg.MaxSteps,
		approver:        approver,
		reasoningEffort: cfg.Reasoning,
		providerCfg: ai.ProviderConfig{
			DenyDataCollection: cfg.DenyDataCollection,
			NoFallbacks:        cfg.NoFallbacks,
			RawJSON:            cfg.OpenRouterProviderJSON,
		},
		logger: logger,
	}
	if logger != nil {
		logger.Write("session_start", map[string]any{
			"provider":           string(cfg.Provider),
			"model":              cfg.Model,
			"base_url":           cfg.BaseURL,
			"max_steps":          cfg.MaxSteps,
			"max_context_tokens": cfg.MaxContextTokens,
			"streaming":          !cfg.NoStream,
			"reasoning":          string(cfg.Reasoning),
		})
	}
	return a
}

// ContextUsage reports the agent's current estimated prompt-token count and
// the configured soft cap. A cap of 0 means compaction is disabled. The used
// value tracks the server's actual prompt_tokens once calibrated; before the
// first completion it uses a char-based estimate.
func (a *Agent) ContextUsage() (used, limit int) {
	return a.history.estimateTokens(), a.history.maxContextTokens
}

// ContextSnapshot summarises the current message history for debugging UIs
// (the /context REPL command). Char counts are JSON-marshalled lengths —
// the same units estimateTokens divides by CharsPerToken. The "system"
// counts cover the very first system message (the original system prompt);
// any elision note inserted by compaction is accounted for separately.
type ContextSnapshot struct {
	SystemPromptChars int
	ElisionNoteChars  int
	UserChars         int
	UserMsgs          int
	AssistantChars    int
	AssistantMsgs     int
	ToolChars         int
	ToolMsgs          int
	TotalChars        int
	EstimatedTokens   int
	MaxContextTokens  int
	CharsPerToken     float64
	Calibrated        bool
}

// ContextSnapshot returns a per-role breakdown of the current history plus
// the calibration ratio used for token estimates. Calibrated is true once
// the server has reported prompt_tokens at least once; before that, the
// default ratio (defaultCharsPerToken) is reported and Calibrated is false.
func (a *Agent) ContextSnapshot() ContextSnapshot {
	h := a.history
	snap := ContextSnapshot{
		EstimatedTokens:  h.estimateTokens(),
		MaxContextTokens: h.maxContextTokens,
		CharsPerToken:    h.charsPerToken,
		Calibrated:       h.charsPerToken > 0,
	}
	if !snap.Calibrated {
		snap.CharsPerToken = defaultCharsPerToken
	}
	for i, m := range h.messages {
		size := messageChars(m)
		snap.TotalChars += size
		switch m.Role {
		case ai.RoleSystem:
			if i == 0 {
				snap.SystemPromptChars = size
			} else {
				snap.ElisionNoteChars += size
			}
		case ai.RoleUser:
			snap.UserChars += size
			snap.UserMsgs++
		case ai.RoleAssistant:
			snap.AssistantChars += size
			snap.AssistantMsgs++
		case ai.RoleTool:
			snap.ToolChars += size
			snap.ToolMsgs++
		}
	}
	return snap
}

func messageChars(m ai.Message) int {
	b, err := m.MarshalJSON()
	if err != nil {
		return 0
	}
	return len(b)
}

// Close releases agent-owned resources (currently the debug log file).
func (a *Agent) Close() error {
	return a.logger.Close()
}

// SetModel changes the model used for subsequent completions. The
// agent serializes per-turn so this is safe to call between turns.
// Calling it mid-turn is undefined — the in-flight request was
// already dispatched with the old model.
func (a *Agent) SetModel(model string) {
	a.model = model
}

// LoadReplay rebuilds the in-memory history from a previously persisted
// transcript. Tool blocks are stripped per the resume contract
// (.scratch/sessions-redesign/01-decisions.md §5): role:"tool" records
// are dropped entirely, and assistant messages lose their tool_calls.
// An assistant message left with no text after stripping is dropped
// too — sending it would produce an empty assistant turn the server
// would reject. No EventMessageAppended is emitted: these messages
// were already persisted on disk, so re-recording them would
// duplicate every line.
//
// Must be called before the agent's first Run; appending replay after
// a turn has already happened mixes pre- and post-resume history in
// an order the model won't understand.
func (a *Agent) LoadReplay(messages []ai.Message) {
	for _, m := range messages {
		switch m.Role {
		case ai.RoleUser:
			a.history.messages = append(a.history.messages, m)
		case ai.RoleAssistant:
			stripped := ai.Message{Role: ai.RoleAssistant, Content: m.Content}
			if stripped.Content == "" {
				continue
			}
			a.history.messages = append(a.history.messages, stripped)
		}
		// role:"tool" and role:"system" are intentionally dropped.
	}
}

// Reset clears the conversation history (keeping the original system prompt)
// and any per-session "always allow" approvals. Used by REPL slash commands
// like /new.
func (a *Agent) Reset() {
	a.history.reset()
	a.approver.Reset()
	a.sessionCost = 0
}

// ResetWithSystemPrompt is Reset, but also replaces the system prompt with
// newPrompt. Used by /new so an edit to AGENTS.md / CLAUDE.md takes effect on
// the next conversation without restarting lambda.
func (a *Agent) ResetWithSystemPrompt(newPrompt string) {
	a.history.resetWithSystemPrompt(newPrompt)
	a.approver.Reset()
	a.sessionCost = 0
}

// Run executes one user turn: append the user message, then loop requesting
// completions and executing tool calls until the model stops calling tools
// or the iteration cap is hit. Events are written to out; out is closed when
// the turn finishes (successfully or otherwise).
func (a *Agent) Run(ctx context.Context, userInput string, out chan<- Event) {
	defer close(out)
	a.appendMessage(ctx, ai.UserMessage(userInput), out)
	a.emitContextUsage(ctx, out)
	a.logger.Write("turn_start", map[string]any{"input_chars": len(userInput)})

	for step := 0; step < a.maxSteps; step++ {
		// Planning-only reasoning policy: only the first turn after the user
		// message reasons; tool-result follow-ups send no reasoning. See
		// docs/adr/0002-reasoning-policy.md.
		effort := ai.ReasoningOff
		if step == 0 {
			effort = a.reasoningEffort
		}
		assistant, err := a.completeOne(ctx, effort, out)
		if err != nil {
			// On ctx cancellation the TUI drives a close-channel signal; don't
			// surface the wrapped-context error as an EventError.
			if ctx.Err() == nil {
				a.emit(ctx, out, EventError{Err: err})
			}
			return
		}
		a.appendMessage(ctx, assistant, out)
		a.emitContextUsage(ctx, out)

		if len(assistant.ToolCalls) == 0 {
			a.emit(ctx, out, EventTurnDone{Reason: "done"})
			return
		}
		for i, tc := range assistant.ToolCalls {
			if !a.handleToolCall(ctx, tc, out) {
				// Cancelled mid-turn. Every tool_call_id on the assistant message
				// must have a paired tool message, or the next request 400s.
				for _, rem := range assistant.ToolCalls[i+1:] {
					a.appendMessage(ctx, ai.ToolMessage("cancelled by user", rem.ID), out)
				}
				a.emitContextUsage(ctx, out)
				return
			}
		}
	}
	a.emit(ctx, out, EventTurnDone{Reason: fmt.Sprintf("hit iteration limit (%d steps)", a.maxSteps)})
}

// completeOne issues one completion request via the Completer and returns the
// assistant message to append to the history. The completer's content/reasoning
// callbacks are wired straight into the agent's event channel.
func (a *Agent) completeOne(ctx context.Context, effort ai.ReasoningEffort, out chan<- Event) (ai.Message, error) {
	a.compactIfNeeded(ctx, out)
	// Snapshot the char count of the exact message set we're sending; pairs
	// with prompt_tokens in the response to calibrate charsPerToken.
	charsSent := a.history.totalChars()

	req := ai.CompletionRequest{
		Model:     a.model,
		Messages:  a.history.messages,
		Tools:     a.tools,
		Reasoning: effort,
		Provider:  a.providerCfg,
	}

	a.logger.Write("request", map[string]any{
		"model":      a.model,
		"msg_count":  len(a.history.messages),
		"chars_sent": charsSent,
		"est_tokens": a.history.estimateTokens(),
		"tool_count": len(a.tools),
	})

	onContent := func(s string) { a.emit(ctx, out, EventContentDelta{Text: s}) }
	onReasoning := func(s string) { a.emit(ctx, out, EventThinkingDelta{Text: s}) }

	res, err := a.completer.Complete(ctx, req, onContent, onReasoning)
	if err != nil {
		if errors.Is(err, ai.ErrNoChoices) {
			a.logger.Write("response_error", map[string]any{"err": "no choices"})
			return ai.Message{}, err
		}
		a.logger.Write("response_error", map[string]any{"err": err.Error()})
		return ai.Message{}, err
	}

	a.history.recordTokenUsage(charsSent, res.PromptTokens)
	a.emitContextUsage(ctx, out)
	if res.Cost > 0 {
		a.sessionCost += res.Cost
		a.emit(ctx, out, EventCost{Turn: res.Cost, Session: a.sessionCost})
	}
	a.logResponse(res.FinishReason, res.Message.Content, res.Reasoning, len(res.Message.ToolCalls), res.PromptTokens, res.CompletionTokens, res.Cost)
	if res.Message.Content != "" {
		a.emit(ctx, out, EventAssistantDone{Text: res.Message.Content})
	}
	return res.Message, nil
}

// logResponse records one model completion to the debug log. Bodies are
// truncated by truncBody so the log stays bounded for huge replies; the full
// size is preserved in the *_chars fields.
func (a *Agent) logResponse(finishReason, content, reasoning string, toolCalls int, promptTokens, completionTokens int64, cost float64) {
	if a.logger == nil {
		return
	}
	a.logger.Write("response", map[string]any{
		"finish_reason":     finishReason,
		"content":           truncBody(content),
		"content_chars":     len(content),
		"reasoning":         truncBody(reasoning),
		"reasoning_chars":   len(reasoning),
		"tool_call_count":   toolCalls,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"cost_usd":          cost,
	})
}

// handleToolCall handles one tool call, including the confirmation flow for
// destructive tools. Returns false if ctx was cancelled before the tool result
// could be recorded (so the caller should stop processing more tool calls).
func (a *Agent) handleToolCall(ctx context.Context, tc ai.ToolCall, out chan<- Event) bool {
	name := tc.Name
	args := tc.Arguments

	if !a.approver.Allow(ctx, name, args) {
		a.emit(ctx, out, EventToolDenied{ID: tc.ID, Name: name})
		a.appendMessage(ctx, ai.ToolMessage("denied this tool call", tc.ID), out)
		a.emitContextUsage(ctx, out)
		return ctx.Err() == nil
	}

	a.emit(ctx, out, EventToolStart{ID: tc.ID, Name: name, Args: args})
	result := a.registry.Execute(ctx, name, args)
	a.emit(ctx, out, EventToolResult{ID: tc.ID, Name: name, Result: result})
	a.appendMessage(ctx, ai.ToolMessage(result, tc.ID), out)
	a.emitContextUsage(ctx, out)

	return ctx.Err() == nil
}

// compactIfNeeded delegates the compaction work to history and writes a log
// record summarising the result when the logger is enabled.
func (a *Agent) compactIfNeeded(ctx context.Context, out chan<- Event) {
	stats := a.history.compactIfNeeded()
	a.emitContextUsage(ctx, out)
	if a.logger == nil || !stats.changed() {
		return
	}
	a.logger.Write("compaction", map[string]any{
		"before_tokens":    stats.beforeTokens,
		"after_tokens":     stats.afterTokens,
		"limit":            a.history.maxContextTokens,
		"turns_dropped":    stats.turnsDropped,
		"tool_msgs_shrunk": stats.shrunk,
		"msgs_before":      stats.msgsBefore,
		"msgs_after":       stats.msgsAfter,
	})
}

// appendMessage extends the in-memory history and emits an
// EventMessageAppended so a surrounding loop can persist the message
// if it cares. The single mutation seam — every message-add path in
// Run / handleToolCall goes through here.
func (a *Agent) appendMessage(ctx context.Context, m ai.Message, out chan<- Event) {
	a.history.append(m)
	a.emit(ctx, out, EventMessageAppended{Message: m})
}

func (a *Agent) emitContextUsage(ctx context.Context, out chan<- Event) {
	used, limit := a.ContextUsage()
	a.emit(ctx, out, EventContextUsage{Used: used, Limit: limit})
}

func (a *Agent) emit(ctx context.Context, out chan<- Event, e Event) {
	if a.logger != nil {
		a.logger.Write(eventFields(e))
	}
	select {
	case <-ctx.Done():
	case out <- e:
	}
}
