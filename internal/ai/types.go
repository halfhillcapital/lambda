package ai

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type Message struct {
	Role       Role
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

func SystemMessage(content string) Message {
	return Message{Role: RoleSystem, Content: content}
}

func UserMessage(content string) Message {
	return Message{Role: RoleUser, Content: content}
}

func AssistantMessage(content string) Message {
	return Message{Role: RoleAssistant, Content: content}
}

func ToolMessage(content, toolCallID string) Message {
	return Message{Role: RoleTool, Content: content, ToolCallID: toolCallID}
}

func (m Message) MarshalJSON() ([]byte, error) {
	type wireToolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type wireMessage struct {
		Role       Role           `json:"role"`
		Content    string         `json:"content,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
		ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	}
	w := wireMessage{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		w.ToolCalls = make([]wireToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			w.ToolCalls[i].ID = tc.ID
			w.ToolCalls[i].Type = "function"
			w.ToolCalls[i].Function.Name = tc.Name
			w.ToolCalls[i].Function.Arguments = tc.Arguments
		}
	}
	return json.Marshal(w)
}

type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type CompletionRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolSpec
}

type CompletionResult struct {
	Message          Message
	Reasoning        string
	FinishReason     string
	PromptTokens     int64
	CompletionTokens int64
}

// Completer hides everything between "ask the model for a completion" and
// "here's the assembled assistant message + usage." Implementations may talk
// to OpenAI-compatible endpoints, native vendor APIs, or test fakes.
//
// Streaming deltas (content tokens, reasoning tokens) are surfaced via the
// onContent / onReasoning callbacks. Either callback may be nil if the caller
// doesn't care.
//
// Implementations must:
//   - return a fully-assembled message (no partial state) on success;
//   - drain their internal stream before returning; no callbacks fire after
//     Complete returns;
//   - honour ctx cancellation: return promptly with the cancellation error or
//     the last partial error, whichever fits.
type Completer interface {
	Complete(
		ctx context.Context,
		req CompletionRequest,
		onContent func(string),
		onReasoning func(string),
	) (CompletionResult, error)
}
