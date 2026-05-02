# lambda

A minimal CLI coding agent for local LLMs via any OpenAI-compatible endpoint
(Ollama, LM Studio, vLLM, TGI, …) with first-class support for
[OpenRouter](https://openrouter.ai/) when you need to reach for a cloud model.

- **REPL** backed by [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  with streaming, markdown rendering, and a confirmation modal for destructive
  tool calls.
- **One-shot mode** via `-p`, positional args, or piped stdin — stdout stays
  clean so it pipes cleanly into other tools.
- **Seven built-in tools**: `read`, `write`, `edit`, `grep`, `glob`, `bash`, `skill`. Destructive ones (`write`, `edit`, `bash`)
  require per-call confirmation (or `--yolo`).
- **Skills**: markdown instructions in `~/.claude/skills/` or `./.claude/skills/` are listed in the system prompt
  and loaded on demand via the `skill` tool. See `docs/agents/skills.md`.
- **Project context**: at startup, lambda walks up from the cwd to the first
  `.git` ancestor and loads `AGENTS.md` (or `CLAUDE.md` as a fallback) from
  the nearest directory that has one. The contents are spliced into the
  system prompt verbatim, capped at 8 KiB (tail-truncated). Re-read on `/new`
  / `/clear` so edits take effect mid-session. Disable with
  `--no-project-context`.
- **REPL slash commands**: `/new` (or `/clear`) to start a fresh conversation,
  `/help` for the list. Anything starting with `/` is treated as a command.
- **History compaction**: long REPL sessions stay under a soft prompt-token
  cap (default 100K) by dropping oldest turns and inserting a system note
  about how many were elided. If a single remaining turn is still over
  budget, the biggest tool-result bodies are trimmed in place. The budget is
  measured against actual `prompt_tokens` reported by the server (with
  char-based estimation as a fallback). Override with `--max-context-tokens`
  (`0` to disable).

## Install / build

```bash
make build          # produces ./bin/lambda
# or directly:
go build -o bin/lambda ./cmd/lambda
```

Requires Go 1.26+ and `bash` on `PATH` (Git Bash on Windows) for the `bash` tool.

## Usage

```bash
# REPL
OPENAI_MODEL=qwen2.5-coder lambda

# one-shot
OPENAI_MODEL=qwen2.5-coder lambda -p "summarize this repo"
echo "fix the bug in main.go" | lambda

# point at LM Studio / vLLM instead of Ollama
OPENAI_BASE_URL=http://localhost:1234/v1 OPENAI_MODEL=qwen2.5-coder lambda
```

| Env var              | Default                       | Notes                              |
|----------------------|-------------------------------|------------------------------------|
| `OPENAI_MODEL`       | *(required)*                  | model ID the server expects        |
| `OPENAI_BASE_URL`    | `http://localhost:11434/v1`   | OpenAI-compatible endpoint         |
| `OPENAI_API_KEY`     | `sk-local`                    | most local servers ignore the value |
| `LAMBDA_PROVIDER`    | `openai-compat`               | or `openrouter`                    |
| `OPENROUTER_API_KEY` | *(unset)*                     | preferred over `OPENAI_API_KEY` when provider is `openrouter` |
| `LAMBDA_REASONING`   | `off`                         | `off`, `low`, `medium`, or `high`  |

Flags override env vars. Run `lambda -h` for the full list.

## OpenRouter

```bash
OPENROUTER_API_KEY=sk-or-... \
OPENAI_MODEL=anthropic/claude-opus-4 \
lambda --provider openrouter
```

When `--provider openrouter` is set, lambda:

- Defaults the base URL to `https://openrouter.ai/api/v1`.
- Reads `OPENROUTER_API_KEY` (falls back to `OPENAI_API_KEY` so existing
  setups keep working).
- Opts in to detailed usage so per-call USD cost is reported in the status
  line and printed to stderr at the end of one-shot runs.
- Does **not** send `HTTP-Referer` / `X-Title` attribution headers — your
  usage stays off OpenRouter's public leaderboard.

Provider routing knobs:

| Flag | Effect |
|------|--------|
| `--no-data-collection` | only route to providers that don't log/train on prompts |
| `--no-fallbacks`       | fail rather than silently route to a fallback provider |
| `--openrouter-provider-json '{...}'` | splice raw JSON into the request `provider` object (overrides the flags above) |

## Reasoning

`--reasoning low|medium|high` (default `off`) sends a `reasoning: {effort: ...}`
hint on requests to reasoning-capable models (Anthropic extended thinking,
OpenAI o-series, DeepSeek-R1, …). To keep token spend bounded, lambda only
sends the reasoning hint on the first model turn after each user message
(the planning turn); follow-up turns that just process tool results omit it.
See `docs/adr/0002-reasoning-policy.md` for the rationale.

## Layout

```
cmd/lambda/      # main package — flag parsing, one-shot runner, entry point
internal/
  agent/         # tool-calling loop, events, model client
  config/        # flag + env parsing
  prompt/        # system prompt assembly
  tools/         # tool schemas, dispatcher, process-group handling
  tui/           # Bubble Tea REPL
```
