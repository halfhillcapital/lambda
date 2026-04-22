// Package agent implements the tool-calling loop that drives a chat model
// through one user turn, emitting events for UI consumers.
package agent

import (
	"context"
	"errors"
	"fmt"
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
	EventAssistantDone struct{ Text string }
	EventToolStart     struct{ ID, Name, Args string }
	EventToolResult    struct{ ID, Name, Result string }
	EventToolDenied    struct{ ID, Name string }
	EventTurnDone      struct{ Reason string }
	EventError         struct{ Err error }
)

func (EventContentDelta) isEvent()  {}
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
	confirmer Confirmer

	allowedTools map[string]bool
	alwaysAll    bool
}

func New(cfg *config.Config, systemPrompt string, confirmer Confirmer) *Agent {
	client := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
	)
	return &Agent{
		client:   client,
		model:    cfg.Model,
		tools:    tools.Schemas(),
		messages: []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(systemPrompt)},
		maxSteps: cfg.MaxSteps,
		noStream: cfg.NoStream,
		yolo:     cfg.Yolo,
		confirmer: func(ctx context.Context, name, args string) Decision {
			if cfg.Yolo {
				return DecisionAllow
			}
			return confirmer(ctx, name, args)
		},
		allowedTools: map[string]bool{},
		alwaysAll:    cfg.Yolo,
	}
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
			emit(ctx, out, EventError{Err: err})
			return
		}
		a.messages = append(a.messages, openai.ChatCompletionMessageParamUnion{OfAssistant: assistant})

		if len(assistant.ToolCalls) == 0 {
			emit(ctx, out, EventTurnDone{Reason: "done"})
			return
		}
		for _, tc := range assistant.ToolCalls {
			if !a.handleToolCall(ctx, tc, out) {
				return // ctx cancelled mid-turn
			}
		}
	}
	emit(ctx, out, EventTurnDone{Reason: fmt.Sprintf("hit iteration limit (%d steps)", a.maxSteps)})
}

// completeOne issues one completion request and returns the assistant message
// to append to the history, streaming content deltas into out along the way.
func (a *Agent) completeOne(ctx context.Context, out chan<- Event) (*openai.ChatCompletionAssistantMessageParam, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(a.model),
		Messages: a.messages,
		Tools:    a.tools,
	}

	if a.noStream {
		comp, err := withTransientRetry(func() (*openai.ChatCompletion, error) {
			return a.client.Chat.Completions.New(ctx, params)
		})
		if err != nil {
			return nil, humanizeError(err)
		}
		if len(comp.Choices) == 0 {
			return nil, errors.New("model returned no choices")
		}
		msg := comp.Choices[0].Message
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
			if delta := chunk.Choices[0].Delta.Content; delta != "" {
				emit(ctx, out, EventContentDelta{Text: delta})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, humanizeError(err)
	}
	if len(acc.Choices) == 0 {
		return nil, errors.New("model returned no choices")
	}
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
		switch a.confirmer(ctx, name, args) {
		case DecisionDeny:
			emit(ctx, out, EventToolDenied{ID: tc.ID, Name: name})
			a.messages = append(a.messages, openai.ToolMessage("user denied this tool call", tc.ID))
			return true
		case DecisionAlwaysTool:
			a.allowedTools[name] = true
		case DecisionAlwaysAll:
			a.alwaysAll = true
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

// withTransientRetry retries a one-off request once on 5xx / connection flaps
// with a 1s backoff.
func withTransientRetry[T any](fn func() (T, error)) (T, error) {
	res, err := fn()
	if err == nil || !isTransient(err) {
		return res, err
	}
	time.Sleep(1 * time.Second)
	return fn()
}

func isTransient(err error) bool {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500
	}
	return false
}

// humanizeError turns transport/auth failures into messages the user can act on.
func humanizeError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
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
