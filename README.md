# lambda

A minimal CLI coding agent for local LLMs via any OpenAI-compatible endpoint
(Ollama, LM Studio, vLLM, TGI, …).

- **REPL** backed by [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  with streaming, markdown rendering, and a confirmation modal for destructive
  tool calls.
- **One-shot mode** via `-p`, positional args, or piped stdin — stdout stays
  clean so it pipes cleanly into other tools.
- **Six built-in tools**: `read`, `write`, `edit`, `grep`, `glob`, `bash`. Destructive ones (`write_file`, `edit_file`, `bash`)
  require per-call confirmation (or `--yolo`).
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

| Env var           | Default                       | Notes                              |
|-------------------|-------------------------------|------------------------------------|
| `OPENAI_MODEL`    | *(required)*                  | model ID the server expects        |
| `OPENAI_BASE_URL` | `http://localhost:11434/v1`   | OpenAI-compatible endpoint         |
| `OPENAI_API_KEY`  | `sk-local`                    | most local servers ignore the value |

Flags override env vars. Run `lambda -h` for the full list.

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
