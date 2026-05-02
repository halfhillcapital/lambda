package agent

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
)

// openAICompleter is the production Completer: it talks HTTP to any
// OpenAI-compatible chat-completions endpoint, retries transient errors,
// stitches streaming chunks via the SDK accumulator, and humanizes the
// transport-layer errors callers will see.
type openAICompleter struct {
	client openai.Client
	stream bool
}

// NewOpenAICompleter constructs a Completer against the given endpoint. When
// stream is true, the adapter uses chat.completions.stream and asks for the
// final usage chunk; when false, it issues a non-streaming request. Local
// inference frameworks that ignore include_usage just leave the calibration
// pinned to its previous value — no failure mode.
func NewOpenAICompleter(baseURL, apiKey string, stream bool) Completer {
	// MaxRetries(0) disables the SDK's retry loop; withTransientRetry below is
	// the canonical retry layer (classifies errors, surfaces failures to the
	// caller, honours ctx cancellation during backoff).
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(newHTTPClient()),
		option.WithMaxRetries(0),
	)
	return &openAICompleter{client: client, stream: stream}
}

// errNoChoices is returned by Complete when the response has zero choices.
// Distinguishable from SDK errors so it bypasses humanizeError (which would
// wrap it as a transport failure).
var errNoChoices = errors.New("model returned no choices")

func (c *openAICompleter) Complete(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	onContent func(string),
	onReasoning func(string),
) (Result, error) {
	if c.stream {
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		}
		return c.completeStreaming(ctx, params, onContent, onReasoning)
	}
	return c.completeNonStreaming(ctx, params, onReasoning)
}

func (c *openAICompleter) completeNonStreaming(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	onReasoning func(string),
) (Result, error) {
	comp, err := withTransientRetry(ctx, func() (*openai.ChatCompletion, error) {
		return c.client.Chat.Completions.New(ctx, params)
	})
	if err != nil {
		return Result{}, humanizeError(err)
	}
	if len(comp.Choices) == 0 {
		return Result{}, errNoChoices
	}
	msg := comp.Choices[0].Message
	reasoning := extractReasoning(msg.JSON.ExtraFields)
	if reasoning != "" && onReasoning != nil {
		onReasoning(reasoning)
	}
	return Result{
		Msg:              msg,
		Reasoning:        reasoning,
		FinishReason:     comp.Choices[0].FinishReason,
		PromptTokens:     comp.Usage.PromptTokens,
		CompletionTokens: comp.Usage.CompletionTokens,
	}, nil
}

func (c *openAICompleter) completeStreaming(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	onContent, onReasoning func(string),
) (Result, error) {
	var (
		acc       openai.ChatCompletionAccumulator
		reasoning strings.Builder
	)
	stream := c.client.Chat.Completions.NewStreaming(ctx, params)
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
		return Result{}, humanizeError(err)
	}
	if len(acc.Choices) == 0 {
		return Result{}, errNoChoices
	}
	return Result{
		Msg:              acc.Choices[0].Message,
		Reasoning:        reasoning.String(),
		FinishReason:     acc.Choices[0].FinishReason,
		PromptTokens:     acc.Usage.PromptTokens,
		CompletionTokens: acc.Usage.CompletionTokens,
	}, nil
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
