package config

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// ErrHelp is returned by Load when the user passed -h/--help.
// Callers should treat this as a successful exit.
var ErrHelp = flag.ErrHelp

type Config struct {
	BaseURL          string
	APIKey           string
	Model            string
	MaxSteps         int
	MaxContextTokens int
	NoStream         bool
	Yolo             bool
	NoWorktree       bool
	Debug            bool   // when true, append a JSONL debug log to debug.jsonl in the cwd
	Prompt           string // -p one-shot prompt
	Args             []string
}

const (
	defaultBaseURL = "http://localhost:11434/v1"
	defaultAPIKey  = "sk-local"
)

func Load() (*Config, error) {
	c := &Config{}

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
  OPENAI_BASE_URL   default %s
  OPENAI_API_KEY    default %q
  OPENAI_MODEL      required; no default

Examples:
  OPENAI_MODEL=qwen2.5-coder lambda
  OPENAI_BASE_URL=http://localhost:1234/v1 OPENAI_MODEL=qwen2.5-coder lambda -p hi
`, defaultBaseURL, defaultAPIKey)
	}

	fs.StringVar(&c.BaseURL, "base-url", "", "OpenAI-compatible base URL")
	fs.StringVar(&c.APIKey, "api-key", "", "API key (local servers usually ignore the value)")
	fs.StringVar(&c.Model, "model", "", "model ID (e.g. qwen2.5-coder, llama3.1:8b)")
	fs.IntVar(&c.MaxSteps, "max-steps", 50, "max tool-call rounds per user turn")
	fs.IntVar(&c.MaxContextTokens, "max-context-tokens", 100_000, "soft cap on prompt tokens; oldest turns are dropped beyond this. 0 disables compaction. Tracked against actual prompt_tokens returned by the server when available; falls back to a char-based estimate otherwise.")
	fs.BoolVar(&c.NoStream, "no-stream", false, "disable streaming; print once complete")
	fs.BoolVar(&c.Yolo, "yolo", false, "skip all confirmation prompts for destructive tools")
	fs.BoolVar(&c.NoWorktree, "no-worktree", false, "run in the current checkout instead of an isolated git worktree")
	fs.BoolVar(&c.Debug, "debug", false, "append JSONL debug records to debug.jsonl in the working directory")
	fs.StringVar(&c.Prompt, "p", "", "one-shot prompt (alias for --prompt)")
	fs.StringVar(&c.Prompt, "prompt", "", "one-shot prompt")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	c.Args = fs.Args()

	if c.BaseURL == "" {
		c.BaseURL = envOr("OPENAI_BASE_URL", defaultBaseURL)
	}
	if c.APIKey == "" {
		c.APIKey = envOr("OPENAI_API_KEY", defaultAPIKey)
	}
	if c.Model == "" {
		c.Model = os.Getenv("OPENAI_MODEL")
	}
	if !c.NoWorktree && envTrue("LAMBDA_NO_WORKTREE") {
		c.NoWorktree = true
	}

	if c.Model == "" {
		return nil, fmt.Errorf(`no model set. Set --model or OPENAI_MODEL.
Common choices:
  Ollama:     qwen2.5-coder, qwen2.5-coder:32b, llama3.1:8b, deepseek-coder-v2
  LM Studio:  whatever you see in the server panel
  vLLM/TGI:   the HF model id you loaded`)
	}

	return c, nil
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
