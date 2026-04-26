// Package agent implements the tool-calling loop that drives a chat model
// through one user turn, emitting events for UI consumers.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"

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
	EventTurnDone      struct{ Reason string }
	EventError         struct{ Err error }
)

func (EventContentDelta) isEvent()  {}
func (EventThinkingDelta) isEvent() {}
func (EventAssistantDone) isEvent() {}
func (EventToolStart) isEvent()     {}
func (EventToolResult) isEvent()    {}
func (EventToolDenied) isEvent()    {}
func (EventTurnDone) isEvent()      {}
func (EventError) isEvent()         {}

// Agent is the main tool-calling loop. It is not safe for concurrent use —
// one turn at a time per agent.
type Agent struct {
	client    openai.Client
	model     string
	tools     []openai.ChatCompletionToolParam
	messages  []openai.ChatCompletionMessageParamUnion
	maxSteps  int
	noStream  bool
	yolo      bool
	policy    Policy
	confirmer Confirmer

	allowedTools map[string]bool
	alwaysAll    bool

	// Conversation compaction state.
	maxContextTokens int     // soft cap on prompt tokens; <=0 disables
	charsPerToken    float64 // running calibration from server-reported prompt_tokens; 0 means no data yet
	droppedTurns     int
	elisionNoteIdx   int // 0 = no note inserted yet
}

// defaultCharsPerToken is the fallback ratio used until the server reports
// actual prompt_tokens. Chat/tool-call JSON tokenizes denser than plain prose,
// so this is conservative on the low side (over-estimates tokens, triggering
// earlier compaction rather than blowing the context window).
const defaultCharsPerToken = 3.5

func New(cfg *config.Config, systemPrompt string, pol Policy, confirmer Confirmer) *Agent {
	// MaxRetries(0) disables the SDK's retry loop; withTransientRetry below is
	// the canonical retry layer (classifies errors, surfaces failures via
	// EventError, honors ctx cancellation during backoff).
	client := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(newHTTPClient()),
		option.WithMaxRetries(0),
	)
	return &Agent{
		client:           client,
		model:            cfg.Model,
		tools:            tools.Schemas(),
		messages:         []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(systemPrompt)},
		maxSteps:         cfg.MaxSteps,
		noStream:         cfg.NoStream,
		yolo:             cfg.Yolo,
		policy:           pol,
		confirmer:        confirmer,
		allowedTools:     map[string]bool{},
		alwaysAll:        cfg.Yolo,
		maxContextTokens: cfg.MaxContextTokens,
	}
}

// ContextUsage reports the agent's current estimated prompt-token count and
// the configured soft cap. A cap of 0 means compaction is disabled. The used
// value tracks the server's actual prompt_tokens once calibrated; before the
// first completion it uses a char-based estimate.
func (a *Agent) ContextUsage() (used, limit int) {
	return a.estimateTokens(), a.maxContextTokens
}

// Reset clears the conversation history (keeping the original system prompt)
// and any per-session "always allow" approvals. Used by REPL slash commands
// like /new.
func (a *Agent) Reset() {
	if len(a.messages) > 0 && roleOf(a.messages[0]) == "system" {
		a.messages = a.messages[:1]
	} else {
		a.messages = a.messages[:0]
	}
	a.droppedTurns = 0
	a.elisionNoteIdx = 0
	a.allowedTools = map[string]bool{}
	a.alwaysAll = a.yolo
}

// Run executes one user turn: append the user message, then loop requesting
// completions and executing tool calls until the model stops calling tools
// or the iteration cap is hit. Events are written to out; out is closed when
// the turn finishes (successfully or otherwise).
func (a *Agent) Run(ctx context.Context, userInput string, out chan<- Event) {
	defer close(out)
	a.messages = append(a.messages, openai.UserMessage(userInput))

	for step := 0; step < a.maxSteps; step++ {
		assistant, err := a.completeOne(ctx, out)
		if err != nil {
			// On ctx cancellation the TUI drives a close-channel signal; don't
			// surface the wrapped-context error as an EventError.
			if ctx.Err() == nil {
				emit(ctx, out, EventError{Err: err})
			}
			return
		}
		a.messages = append(a.messages, openai.ChatCompletionMessageParamUnion{OfAssistant: assistant})

		if len(assistant.ToolCalls) == 0 {
			emit(ctx, out, EventTurnDone{Reason: "done"})
			return
		}
		for i, tc := range assistant.ToolCalls {
			if !a.handleToolCall(ctx, tc, out) {
				// Cancelled mid-turn. Every tool_call_id on the assistant message
				// must have a paired tool message, or the next request 400s.
				for _, rem := range assistant.ToolCalls[i+1:] {
					a.messages = append(a.messages, openai.ToolMessage("cancelled by user", rem.ID))
				}
				return
			}
		}
	}
	emit(ctx, out, EventTurnDone{Reason: fmt.Sprintf("hit iteration limit (%d steps)", a.maxSteps)})
}

// completeOne issues one completion request and returns the assistant message
// to append to the history, streaming content deltas into out along the way.
func (a *Agent) completeOne(ctx context.Context, out chan<- Event) (*openai.ChatCompletionAssistantMessageParam, error) {
	a.compactIfNeeded()
	// Snapshot the char count of the exact message set we're sending; pairs
	// with prompt_tokens in the response to calibrate charsPerToken.
	charsSent := a.totalChars()

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(a.model),
		Messages: a.messages,
		Tools:    a.tools,
	}
	if !a.noStream {
		// Ask servers that support it to emit a final usage chunk. Local
		// inference frameworks ignore this silently; calibration just stays
		// pinned to whatever value we had before.
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		}
	}

	if a.noStream {
		comp, err := withTransientRetry(ctx, func() (*openai.ChatCompletion, error) {
			return a.client.Chat.Completions.New(ctx, params)
		})
		if err != nil {
			return nil, humanizeError(err)
		}
		if len(comp.Choices) == 0 {
			return nil, errors.New("model returned no choices")
		}
		a.recordTokenUsage(charsSent, comp.Usage.PromptTokens)
		msg := comp.Choices[0].Message
		if r := extractReasoning(msg.JSON.ExtraFields); r != "" {
			emit(ctx, out, EventThinkingDelta{Text: r})
		}
		if msg.Content != "" {
			emit(ctx, out, EventAssistantDone{Text: msg.Content})
		}
		return assistantFromMessage(msg), nil
	}

	var acc openai.ChatCompletionAccumulator
	stream := a.client.Chat.Completions.NewStreaming(ctx, params)
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if r := extractReasoning(delta.JSON.ExtraFields); r != "" {
				emit(ctx, out, EventThinkingDelta{Text: r})
			}
			if delta.Content != "" {
				emit(ctx, out, EventContentDelta{Text: delta.Content})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, humanizeError(err)
	}
	if len(acc.Choices) == 0 {
		return nil, errors.New("model returned no choices")
	}
	a.recordTokenUsage(charsSent, acc.Usage.PromptTokens)
	msg := acc.Choices[0].Message
	if msg.Content != "" {
		emit(ctx, out, EventAssistantDone{Text: msg.Content})
	}
	return assistantFromMessage(msg), nil
}

// handleToolCall handles one tool call, including the confirmation flow for
// destructive tools. Returns false if ctx was cancelled before the tool result
// could be recorded (so the caller should stop processing more tool calls).
func (a *Agent) handleToolCall(ctx context.Context, tc openai.ChatCompletionMessageToolCallParam, out chan<- Event) bool {
	name := tc.Function.Name
	args := tc.Function.Arguments

	if tools.Name(name).IsDestructive() && !a.alwaysAll && !a.allowedTools[name] {
		denied := func(reason string) bool {
			emit(ctx, out, EventToolDenied{ID: tc.ID, Name: name})
			a.messages = append(a.messages, openai.ToolMessage(reason+" denied this tool call", tc.ID))
			return true
		}
		switch a.policy(name, args) {
		case AutoAllow:
			// Skip the user prompt entirely.
		case AutoDeny:
			return denied("policy")
		default:
			switch a.confirmer(ctx, name, args) {
			case DecisionDeny:
				return denied("user")
			case DecisionAlwaysTool:
				a.allowedTools[name] = true
			case DecisionAlwaysAll:
				a.alwaysAll = true
			}
		}
	}

	emit(ctx, out, EventToolStart{ID: tc.ID, Name: name, Args: args})
	result := tools.Execute(ctx, name, args)
	emit(ctx, out, EventToolResult{ID: tc.ID, Name: name, Result: result})
	a.messages = append(a.messages, openai.ToolMessage(result, tc.ID))

	return ctx.Err() == nil
}

// assistantFromMessage converts a response ChatCompletionMessage into the
// param form needed for the next request's message history.
func assistantFromMessage(msg openai.ChatCompletionMessage) *openai.ChatCompletionAssistantMessageParam {
	p := &openai.ChatCompletionAssistantMessageParam{}
	if msg.Content != "" {
		p.Content.OfString = param.NewOpt(msg.Content)
	}
	if len(msg.ToolCalls) > 0 {
		p.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			p.ToolCalls[i] = openai.ChatCompletionMessageToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			}
		}
	}
	return p
}

// newHTTPClient returns an http.Client with bounded connect and response-header
// timeouts but no overall Timeout: streaming completions from local LLMs can
// legitimately run for minutes, so we only want to catch a stuck dial or a
// server that never starts responding. The user can Ctrl+C otherwise.
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &http.Client{Transport: transport}
}

// retryBackoffs are the inter-attempt delays for withTransientRetry.
// len(retryBackoffs)+1 attempts are made in total. Exposed as a package var
// so tests can shorten it.
var retryBackoffs = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// withTransientRetry calls fn, retrying on transient errors (5xx, 408, 429,
// EOF, connection resets) with exponential backoff + ±25% jitter. Honors ctx
// cancellation: returns the last result/error immediately if ctx is done
// during a backoff sleep.
func withTransientRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var (
		res T
		err error
	)
	for attempt := 0; ; attempt++ {
		res, err = fn()
		if err == nil || !isTransient(err) || attempt >= len(retryBackoffs) {
			return res, err
		}
		timer := time.NewTimer(jitter(retryBackoffs[attempt]))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return res, err
		}
	}
}

// jitter returns d perturbed by ±25%.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return time.Duration(float64(d) * (0.75 + rand.Float64()*0.5))
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if apiErr, ok := errors.AsType[*openai.Error](err); ok {
		switch apiErr.StatusCode {
		case 408, 429:
			return true
		}
		return apiErr.StatusCode >= 500
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
	_, ok := errors.AsType[*net.OpError](err)
	return ok
}

// humanizeError turns transport/auth failures into messages the user can act on.
func humanizeError(err error) error {
	if err == nil {
		return nil
	}
	if apiErr, ok := errors.AsType[*openai.Error](err); ok {
		switch apiErr.StatusCode {
		case 401, 403:
			return fmt.Errorf("auth failed (%d): %s — check OPENAI_API_KEY", apiErr.StatusCode, apiErr.Message)
		case 404:
			return fmt.Errorf("not found (%d): %s — is the model name correct?", apiErr.StatusCode, apiErr.Message)
		case 400:
			return fmt.Errorf("bad request (%d): %s", apiErr.StatusCode, apiErr.Message)
		}
		return fmt.Errorf("api error (%d): %s", apiErr.StatusCode, apiErr.Message)
	}
	return fmt.Errorf("request failed: %w (is your local server running?)", err)
}

func emit(ctx context.Context, out chan<- Event, e Event) {
	select {
	case <-ctx.Done():
	case out <- e:
	}
}
