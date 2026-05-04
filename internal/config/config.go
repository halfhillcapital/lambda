package config

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"lambda/internal/ai"
)

// ErrHelp is returned by Load when the user passed -h/--help.
// Callers should treat this as a successful exit.
var ErrHelp = flag.ErrHelp

type Config struct {
	Provider         ai.Provider
	BaseURL          string
	APIKey           string
	Model            string
	MaxSteps         int
	MaxContextTokens int
	NoStream         bool
	Yolo             bool
	NoWorktree       bool
	NoProjectContext bool
	Debug            bool   // when true, append a JSONL debug log to debug.jsonl in the cwd
	Prompt           string // -p one-shot prompt
	Resume           string // --resume <prefix>: reattach to a previously persisted Session
	Args             []string

	// Reasoning is the configured per-request reasoning effort. The agent
	// loop's planning-only policy decides which turns actually send it.
	Reasoning ai.ReasoningEffort

	// OpenRouter-specific routing knobs (ignored when Provider is not openrouter).
	DenyDataCollection      bool
	NoFallbacks             bool
	OpenRouterProviderJSON  string
}

const (
	defaultLocalBaseURL = "http://localhost:11434/v1"
	defaultAPIKey       = "sk-local"
)

func Load() (*Config, error) {
	c := &Config{}

	var (
		providerStr  string
		reasoningStr string
	)

	fs := flag.NewFlagSet("lambda", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `lambda — a minimal CLI coding agent for local LLMs via OpenAI-compatible endpoints.

Usage:
  lambda                          # interactive REPL
  lambda "fix the bug in main.go" # one-shot (positional)
  lambda -p "summarize this repo" # one-shot (flag)
  echo "task" | lambda            # one-shot (stdin)

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Env vars (flags override):
  OPENAI_BASE_URL    default %s
  OPENAI_API_KEY     default %q
  OPENAI_MODEL       required; no default
  LAMBDA_PROVIDER    openai-compat (default) or openrouter
  OPENROUTER_API_KEY preferred over OPENAI_API_KEY when --provider openrouter
  LAMBDA_REASONING   off (default), low, medium, or high

Examples:
  OPENAI_MODEL=qwen2.5-coder lambda
  OPENAI_BASE_URL=http://localhost:1234/v1 OPENAI_MODEL=qwen2.5-coder lambda -p hi
  OPENROUTER_API_KEY=sk-or-... OPENAI_MODEL=anthropic/claude-opus-4 lambda --provider openrouter
`, defaultLocalBaseURL, defaultAPIKey)
	}

	fs.StringVar(&providerStr, "provider", "", `backend mode: "openai-compat" (default) or "openrouter"`)
	fs.StringVar(&c.BaseURL, "base-url", "", "OpenAI-compatible base URL")
	fs.StringVar(&c.APIKey, "api-key", "", "API key (local servers usually ignore the value)")
	fs.StringVar(&c.Model, "model", "", "model ID (e.g. qwen2.5-coder, llama3.1:8b, anthropic/claude-opus-4)")
	fs.IntVar(&c.MaxSteps, "max-steps", 50, "max tool-call rounds per user turn")
	fs.IntVar(&c.MaxContextTokens, "max-context-tokens", 100_000, "soft cap on prompt tokens; oldest turns are dropped beyond this. 0 disables compaction. Tracked against actual prompt_tokens returned by the server when available; falls back to a char-based estimate otherwise.")
	fs.BoolVar(&c.NoStream, "no-stream", false, "disable streaming; print once complete")
	fs.BoolVar(&c.Yolo, "yolo", false, "skip all confirmation prompts for destructive tools")
	fs.BoolVar(&c.NoWorktree, "no-worktree", false, "run in the current checkout instead of an isolated git worktree")
	fs.BoolVar(&c.NoProjectContext, "no-project-context", false, "do not auto-load AGENTS.md / CLAUDE.md from the project")
	fs.BoolVar(&c.Debug, "debug", false, "append JSONL debug records to debug.jsonl in the working directory")
	fs.StringVar(&c.Prompt, "p", "", "one-shot prompt (alias for --prompt)")
	fs.StringVar(&c.Prompt, "prompt", "", "one-shot prompt")
	fs.StringVar(&c.Resume, "resume", "", "resume a persisted session by id or title prefix")
	fs.StringVar(&reasoningStr, "reasoning", "", `reasoning effort: "off" (default), "low", "medium", or "high"`)
	fs.BoolVar(&c.DenyDataCollection, "no-data-collection", false, "openrouter: only route to providers that don't log/train on prompts")
	fs.BoolVar(&c.NoFallbacks, "no-fallbacks", false, "openrouter: fail rather than silently route to a fallback provider")
	fs.StringVar(&c.OpenRouterProviderJSON, "openrouter-provider-json", "", `openrouter: raw JSON spliced into the request "provider" object (overrides --no-data-collection / --no-fallbacks)`)

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	c.Args = fs.Args()

	if providerStr == "" {
		providerStr = os.Getenv("LAMBDA_PROVIDER")
	}
	provider, err := parseProvider(providerStr)
	if err != nil {
		return nil, err
	}
	c.Provider = provider

	if c.BaseURL == "" {
		c.BaseURL = envOr("OPENAI_BASE_URL", "")
	}
	if c.BaseURL == "" {
		if def := provider.DefaultBaseURL(); def != "" {
			c.BaseURL = def
		} else {
			c.BaseURL = defaultLocalBaseURL
		}
	}
	if c.APIKey == "" {
		c.APIKey = resolveAPIKey(provider)
	}
	if c.Model == "" {
		c.Model = os.Getenv("OPENAI_MODEL")
	}
	if !c.NoWorktree && envTrue("LAMBDA_NO_WORKTREE") {
		c.NoWorktree = true
	}

	if reasoningStr == "" {
		reasoningStr = os.Getenv("LAMBDA_REASONING")
	}
	reasoning, err := parseReasoning(reasoningStr)
	if err != nil {
		return nil, err
	}
	c.Reasoning = reasoning

	if c.Model == "" {
		return nil, fmt.Errorf(`no model set. Set --model or OPENAI_MODEL.
Common choices:
  Ollama:     qwen2.5-coder, qwen2.5-coder:32b, llama3.1:8b, deepseek-coder-v2
  LM Studio:  whatever you see in the server panel
  vLLM/TGI:   the HF model id you loaded
  OpenRouter: anthropic/claude-opus-4, openai/gpt-5, etc. (with --provider openrouter)`)
	}

	return c, nil
}

func parseProvider(s string) (ai.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "openai-compat", "openai", "compat":
		return ai.ProviderOpenAICompat, nil
	case "openrouter":
		return ai.ProviderOpenRouter, nil
	}
	return "", fmt.Errorf("unknown provider %q (want openai-compat or openrouter)", s)
}

func parseReasoning(s string) (ai.ReasoningEffort, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "none", "false", "0":
		return ai.ReasoningOff, nil
	case "low":
		return ai.ReasoningLow, nil
	case "medium", "med":
		return ai.ReasoningMedium, nil
	case "high":
		return ai.ReasoningHigh, nil
	}
	return "", fmt.Errorf("unknown reasoning effort %q (want off, low, medium, or high)", s)
}

// resolveAPIKey picks the right env var for the provider, falling back to
// OPENAI_API_KEY so existing setups that point OPENAI_* at OpenRouter keep
// working without renaming env vars.
func resolveAPIKey(p ai.Provider) string {
	if p == ai.ProviderOpenRouter {
		if v := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); v != "" {
			return v
		}
	}
	return envOr("OPENAI_API_KEY", defaultAPIKey)
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envTrue(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}
