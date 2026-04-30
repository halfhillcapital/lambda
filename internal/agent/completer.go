package agent

import (
	"context"

	"github.com/openai/openai-go"
)

// Completer hides everything between "ask the model for a completion" and
// "here's the assembled assistant message + usage." Its single concrete
// implementation is the OpenAI adapter (completer_openai.go); tests can
// substitute a fake that ignores params and returns a canned Result without
// going through HTTP, SSE, retry, or the openai-go SDK at all.
//
// Streaming deltas (content tokens, reasoning tokens) are surfaced via the
// onContent / onReasoning callbacks, which the agent wires to its event
// channel. Either callback may be nil if the caller doesn't care.
//
// Implementations must:
//   - return a fully-assembled message (no partial state) on success;
//   - drain their internal stream before returning; no callbacks fire after
//     Complete returns;
//   - honour ctx cancellation — return promptly with the cancellation
//     error or the last partial error, whichever fits.
type Completer interface {
	Complete(
		ctx context.Context,
		params openai.ChatCompletionNewParams,
		onContent func(string),
		onReasoning func(string),
	) (Result, error)
}

// Result is the canonical outcome of one completion request, uniform across
// streaming and non-streaming adapters. The msg is the fully-assembled
// assistant message ready to append to history (after assistantFromMessage
// converts it to the param shape).
type Result struct {
	Msg              openai.ChatCompletionMessage
	Reasoning        string // captured for the response log record
	FinishReason     string
	PromptTokens     int64
	CompletionTokens int64
}
