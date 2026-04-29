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
	"strings"
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
	client   openai.Client
	model    string
	registry tools.Registry
	tools    []openai.ChatCompletionToolParam
	history  *history
	maxSteps int
	noStream bool
	approver *Approver

	logger *Logger // nil when --debug is off
}

// New constructs an Agent. registry is the tool registry the agent
// dispatches against (use tools.Default for production). approver is the
// single owner of destructive-tool approval (build it with NewApprover).
// logger may be nil to disable structured logging; pair with OpenDebugLog
// if --debug is on. The agent takes ownership of logger and closes it via
// Close.
func New(cfg *config.Config, systemPrompt string, registry tools.Registry, approver *Approver, logger *Logger) *Agent {
	// MaxRetries(0) disables the SDK's retry loop; withTransientRetry below is
	// the canonical retry layer (classifies errors, surfaces failures via
	// EventError, honors ctx cancellation during backoff).
	client := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(newHTTPClient()),
		option.WithMaxRetries(0),
	)
	a := &Agent{
		client:   client,
		model:    cfg.Model,
		registry: registry,
		tools:    registry.Schemas(),
		history:  newHistory(systemPrompt, cfg.MaxContextTokens),
		maxSteps: cfg.MaxSteps,
		noStream: cfg.NoStream,
		approver: approver,
		logger:   logger,
	}
	if logger != nil {
		logger.Write("session_start", map[string]any{
			"model":              cfg.Model,
			"base_url":           cfg.BaseURL,
			"max_steps":          cfg.MaxSteps,
			"max_context_tokens": cfg.MaxContextTokens,
			"streaming":          !cfg.NoStream,
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

// Close releases agent-owned resources (currently the debug log file).
func (a *Agent) Close() error {
	return a.logger.Close()
}

// Reset clears the conversation history (keeping the original system prompt)
// and any per-session "always allow" approvals. Used by REPL slash commands
// like /new.
func (a *Agent) Reset() {
	a.history.reset()
	a.approver.Reset()
}

// Run executes one user turn: append the user message, then loop requesting
// completions and executing tool calls until the model stops calling tools
// or the iteration cap is hit. Events are written to out; out is closed when
// the turn finishes (successfully or otherwise).
func (a *Agent) Run(ctx context.Context, userInput string, out chan<- Event) {
	defer close(out)
	a.history.messages = append(a.history.messages, openai.UserMessage(userInput))
	a.logger.Write("turn_start", map[string]any{"input_chars": len(userInput)})

	for step := 0; step < a.maxSteps; step++ {
		assistant, err := a.completeOne(ctx, out)
		if err != nil {
			// On ctx cancellation the TUI drives a close-channel signal; don't
			// surface the wrapped-context error as an EventError.
			if ctx.Err() == nil {
				a.emit(ctx, out, EventError{Err: err})
			}
			return
		}
		a.history.messages = append(a.history.messages, openai.ChatCompletionMessageParamUnion{OfAssistant: assistant})

		if len(assistant.ToolCalls) == 0 {
			a.emit(ctx, out, EventTurnDone{Reason: "done"})
			return
		}
		for i, tc := range assistant.ToolCalls {
			if !a.handleToolCall(ctx, tc, out) {
				// Cancelled mid-turn. Every tool_call_id on the assistant message
				// must have a paired tool message, or the next request 400s.
				for _, rem := range assistant.ToolCalls[i+1:] {
					a.history.messages = append(a.history.messages, openai.ToolMessage("cancelled by user", rem.ID))
				}
				return
			}
		}
	}
	a.emit(ctx, out, EventTurnDone{Reason: fmt.Sprintf("hit iteration limit (%d steps)", a.maxSteps)})
}

// fetchResult is the canonical outcome of one completion request,
// uniform across streaming and non-streaming modes.
type fetchResult struct {
	msg              openai.ChatCompletionMessage
	reasoning        string // captured for the response log record
	finishReason     string
	promptTokens     int64
	completionTokens int64
}

// errNoChoices is returned by fetchCompletion when the response has zero
// choices. Caller-distinguishable from SDK errors so it bypasses
// humanizeError (which would wrap it as a transport failure).
var errNoChoices = errors.New("model returned no choices")

// completeOne issues one completion request and returns the assistant message
// to append to the history. Streaming-vs-non hides behind fetchCompletion;
// post-processing is uniform.
func (a *Agent) completeOne(ctx context.Context, out chan<- Event) (*openai.ChatCompletionAssistantMessageParam, error) {
	a.compactIfNeeded()
	// Snapshot the char count of the exact message set we're sending; pairs
	// with prompt_tokens in the response to calibrate charsPerToken.
	charsSent := a.history.totalChars()

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(a.model),
		Messages: a.history.messages,
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

	a.logger.Write("request", map[string]any{
		"model":      a.model,
		"msg_count":  len(a.history.messages),
		"chars_sent": charsSent,
		"est_tokens": a.history.estimateTokens(),
		"streaming":  !a.noStream,
		"tool_count": len(a.tools),
	})

	res, err := a.fetchCompletion(ctx, params, out)
	if err != nil {
		if errors.Is(err, errNoChoices) {
			a.logger.Write("response_error", map[string]any{"err": "no choices"})
			return nil, err
		}
		a.logger.Write("response_error", map[string]any{"err": err.Error()})
		return nil, humanizeError(err)
	}

	a.history.recordTokenUsage(charsSent, res.promptTokens)
	a.logResponse(res.finishReason, res.msg.Content, res.reasoning, len(res.msg.ToolCalls), res.promptTokens, res.completionTokens)
	if res.msg.Content != "" {
		a.emit(ctx, out, EventAssistantDone{Text: res.msg.Content})
	}
	return assistantFromMessage(res.msg), nil
}

// fetchCompletion issues one completion request and returns its canonical
// outcome. Streaming and non-streaming paths both emit content/thinking
// deltas on out *during* the fetch, so post-processing in completeOne is
// uniform: no events emitted post-fetch except the final AssistantDone.
func (a *Agent) fetchCompletion(ctx context.Context, params openai.ChatCompletionNewParams, out chan<- Event) (fetchResult, error) {
	if a.noStream {
		comp, err := withTransientRetry(ctx, func() (*openai.ChatCompletion, error) {
			return a.client.Chat.Completions.New(ctx, params)
		})
		if err != nil {
			return fetchResult{}, err
		}
		if len(comp.Choices) == 0 {
			return fetchResult{}, errNoChoices
		}
		msg := comp.Choices[0].Message
		reasoning := extractReasoning(msg.JSON.ExtraFields)
		if reasoning != "" {
			a.emit(ctx, out, EventThinkingDelta{Text: reasoning})
		}
		return fetchResult{
			msg:              msg,
			reasoning:        reasoning,
			finishReason:     comp.Choices[0].FinishReason,
			promptTokens:     comp.Usage.PromptTokens,
			completionTokens: comp.Usage.CompletionTokens,
		}, nil
	}

	var (
		acc       openai.ChatCompletionAccumulator
		reasoning strings.Builder
	)
	stream := a.client.Chat.Completions.NewStreaming(ctx, params)
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			if r := extractReasoning(delta.JSON.ExtraFields); r != "" {
				if a.logger != nil {
					reasoning.WriteString(r)
				}
				a.emit(ctx, out, EventThinkingDelta{Text: r})
			}
			if delta.Content != "" {
				a.emit(ctx, out, EventContentDelta{Text: delta.Content})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return fetchResult{}, err
	}
	if len(acc.Choices) == 0 {
		return fetchResult{}, errNoChoices
	}
	return fetchResult{
		msg:              acc.Choices[0].Message,
		reasoning:        reasoning.String(),
		finishReason:     acc.Choices[0].FinishReason,
		promptTokens:     acc.Usage.PromptTokens,
		completionTokens: acc.Usage.CompletionTokens,
	}, nil
}

// logResponse records one model completion to the debug log. Bodies are
// truncated by truncBody so the log stays bounded for huge replies; the full
// size is preserved in the *_chars fields.
func (a *Agent) logResponse(finishReason, content, reasoning string, toolCalls int, promptTokens, completionTokens int64) {
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
	})
}

// handleToolCall handles one tool call, including the confirmation flow for
// destructive tools. Returns false if ctx was cancelled before the tool result
// could be recorded (so the caller should stop processing more tool calls).
func (a *Agent) handleToolCall(ctx context.Context, tc openai.ChatCompletionMessageToolCallParam, out chan<- Event) bool {
	name := tc.Function.Name
	args := tc.Function.Arguments

	if t, known := a.registry[name]; known && t.IsDestructive() && !a.approver.Allow(ctx, name, args) {
		a.emit(ctx, out, EventToolDenied{ID: tc.ID, Name: name})
		a.history.messages = append(a.history.messages, openai.ToolMessage("denied this tool call", tc.ID))
		return ctx.Err() == nil
	}

	a.emit(ctx, out, EventToolStart{ID: tc.ID, Name: name, Args: args})
	result := a.registry.Execute(ctx, name, args)
	a.emit(ctx, out, EventToolResult{ID: tc.ID, Name: name, Result: result})
	a.history.messages = append(a.history.messages, openai.ToolMessage(result, tc.ID))

	return ctx.Err() == nil
}

// compactIfNeeded delegates the compaction work to history and writes a log
// record summarising the result when the logger is enabled.
func (a *Agent) compactIfNeeded() {
	stats := a.history.compactIfNeeded()
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

func (a *Agent) emit(ctx context.Context, out chan<- Event, e Event) {
	if a.logger != nil {
		a.logger.Write(eventFields(e))
	}
	select {
	case <-ctx.Done():
	case out <- e:
	}
}
