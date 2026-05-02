package ai

import (
	"context"
	"encoding/json"
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
	"github.com/openai/openai-go/packages/respjson"
	"github.com/openai/openai-go/shared"
)

// openAICompleter is the production Completer: it talks HTTP to any
// OpenAI-compatible chat-completions endpoint, retries transient errors,
// stitches streaming chunks via the SDK accumulator, and humanizes the
// transport-layer errors callers will see. The provider field switches small
// per-backend deltas (request shaping, cost extraction) without forking the
// type — every backend lambda supports today is OpenAI-compatible.
type openAICompleter struct {
	client   openai.Client
	stream   bool
	provider Provider
}

// NewOpenAICompleter constructs a Completer against the given endpoint. When
// stream is true, the adapter uses chat.completions.stream and asks for the
// final usage chunk; when false, it issues a non-streaming request. Local
// inference frameworks that ignore include_usage just leave the calibration
// pinned to its previous value — no failure mode.
func NewOpenAICompleter(provider Provider, baseURL, apiKey string, stream bool) Completer {
	// MaxRetries(0) disables the SDK's retry loop; withTransientRetry below is
	// the canonical retry layer (classifies errors, surfaces failures to the
	// caller, honours ctx cancellation during backoff).
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(newHTTPClient()),
		option.WithMaxRetries(0),
	)
	return &openAICompleter{client: client, stream: stream, provider: provider}
}

// ErrNoChoices is returned by Complete when the response has zero choices.
// Distinguishable from SDK errors so it bypasses humanizeError (which would
// wrap it as a transport failure).
var ErrNoChoices = errors.New("model returned no choices")

func (c *openAICompleter) Complete(
	ctx context.Context,
	req CompletionRequest,
	onContent func(string),
	onReasoning func(string),
) (CompletionResult, error) {
	params := openAIParams(req)
	opts, err := c.requestOptions(req)
	if err != nil {
		return CompletionResult{}, err
	}
	if c.stream {
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		}
		return c.completeStreaming(ctx, params, opts, onContent, onReasoning)
	}
	return c.completeNonStreaming(ctx, params, opts, onReasoning)
}

// requestOptions assembles the per-request options that splice provider-
// specific fields into the JSON body. Returns nil opts and nil error when
// nothing needs splicing (the common path for plain openai-compat).
func (c *openAICompleter) requestOptions(req CompletionRequest) ([]option.RequestOption, error) {
	var opts []option.RequestOption
	if req.Reasoning != ReasoningOff {
		opts = append(opts, option.WithJSONSet("reasoning", map[string]any{
			"effort": string(req.Reasoning),
		}))
	}
	if c.provider == ProviderOpenRouter {
		// Always opt in to detailed usage so cost is reported on every call.
		opts = append(opts, option.WithJSONSet("usage", map[string]any{"include": true}))
		obj, err := openRouterProviderObject(req.Provider)
		if err != nil {
			return nil, err
		}
		if obj != nil {
			opts = append(opts, option.WithJSONSet("provider", obj))
		}
	}
	return opts, nil
}

func (c *openAICompleter) completeNonStreaming(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	opts []option.RequestOption,
	onReasoning func(string),
) (CompletionResult, error) {
	comp, err := withTransientRetry(ctx, func() (*openai.ChatCompletion, error) {
		return c.client.Chat.Completions.New(ctx, params, opts...)
	})
	if err != nil {
		return CompletionResult{}, humanizeError(err)
	}
	if len(comp.Choices) == 0 {
		return CompletionResult{}, ErrNoChoices
	}
	msg := comp.Choices[0].Message
	reasoning := extractReasoning(msg.JSON.ExtraFields)
	if reasoning != "" && onReasoning != nil {
		onReasoning(reasoning)
	}
	return CompletionResult{
		Message:          messageFromOpenAI(msg),
		Reasoning:        reasoning,
		FinishReason:     comp.Choices[0].FinishReason,
		PromptTokens:     comp.Usage.PromptTokens,
		CompletionTokens: comp.Usage.CompletionTokens,
		Cost:             c.costFrom(comp.Usage.JSON.ExtraFields),
	}, nil
}

func (c *openAICompleter) completeStreaming(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	opts []option.RequestOption,
	onContent, onReasoning func(string),
) (CompletionResult, error) {
	var (
		acc       openai.ChatCompletionAccumulator
		reasoning strings.Builder
	)
	stream := c.client.Chat.Completions.NewStreaming(ctx, params, opts...)
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if r := extractReasoning(delta.JSON.ExtraFields); r != "" {
			reasoning.WriteString(r)
			if onReasoning != nil {
				onReasoning(r)
			}
		}
		if delta.Content != "" && onContent != nil {
			onContent(delta.Content)
		}
	}
	if err := stream.Err(); err != nil {
		return CompletionResult{}, humanizeError(err)
	}
	if len(acc.Choices) == 0 {
		return CompletionResult{}, ErrNoChoices
	}
	return CompletionResult{
		Message:          messageFromOpenAI(acc.Choices[0].Message),
		Reasoning:        reasoning.String(),
		FinishReason:     acc.Choices[0].FinishReason,
		PromptTokens:     acc.Usage.PromptTokens,
		CompletionTokens: acc.Usage.CompletionTokens,
		Cost:             c.costFrom(acc.Usage.JSON.ExtraFields),
	}, nil
}

// costFrom reads usage.cost only for providers that report it. Avoids a stray
// scan on every plain openai-compat response.
func (c *openAICompleter) costFrom(extras map[string]respjson.Field) float64 {
	if c.provider != ProviderOpenRouter {
		return 0
	}
	return extractCost(extras)
}

func openAIParams(req CompletionRequest) openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(req.Model),
		Messages: openAIMessages(req.Messages),
		Tools:    openAITools(req.Tools),
	}
}

func openAIMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case RoleSystem:
			out = append(out, openai.SystemMessage(m.Content))
		case RoleUser:
			out = append(out, openai.UserMessage(m.Content))
		case RoleAssistant:
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: openAIAssistantMessage(m)})
		case RoleTool:
			out = append(out, openai.ToolMessage(m.Content, m.ToolCallID))
		}
	}
	return out
}

func openAIAssistantMessage(m Message) *openai.ChatCompletionAssistantMessageParam {
	p := &openai.ChatCompletionAssistantMessageParam{}
	if m.Content != "" {
		p.Content.OfString = param.NewOpt(m.Content)
	}
	if len(m.ToolCalls) > 0 {
		p.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			p.ToolCalls[i] = openai.ChatCompletionMessageToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			}
		}
	}
	return p
}

func openAITools(tools []ToolSpec) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

func messageFromOpenAI(msg openai.ChatCompletionMessage) Message {
	m := Message{Role: RoleAssistant, Content: msg.Content}
	if len(msg.ToolCalls) > 0 {
		m.ToolCalls = make([]ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			m.ToolCalls[i] = ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			}
		}
	}
	return m
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
// EOF, connection resets) with exponential backoff + ±25% jitter. Honours ctx
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

// reasoningFieldKeys names the non-spec JSON fields that OpenAI-compatible
// servers use to expose chain-of-thought out-of-band: vLLM/SGLang/DeepSeek/
// recent LM Studio populate "reasoning_content"; Ollama uses "thinking".
// Reading these directly avoids any per-model inline-tag parsing.
var reasoningFieldKeys = []string{"reasoning_content", "thinking"}

// extractReasoning concatenates any reasoning text the server attached to a
// streaming delta or message via the well-known extra fields. Returns "" when
// the server doesn't expose reasoning out-of-band.
func extractReasoning(extras map[string]respjson.Field) string {
	if len(extras) == 0 {
		return ""
	}
	var b strings.Builder
	for _, key := range reasoningFieldKeys {
		f, ok := extras[key]
		if !ok {
			continue
		}
		raw := f.Raw()
		if raw == "" || raw == "null" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			b.WriteString(s)
		}
	}
	return b.String()
}
