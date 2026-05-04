# lambda

A local-first CLI coding agent that talks to OpenAI-compatible chat-completion endpoints. Most users point it at a local inference server (Ollama, LM Studio, vLLM); a smaller cohort uses cloud aggregators like OpenRouter for planning or large tasks.

## Language

**Provider**:
A configured backend mode that determines request shaping, auth env var, and response parsing. Today: `openai-compat` (default, used for all local servers and direct OpenAI) and `openrouter`.
_Avoid_: Backend, vendor, model host.

**Completer**:
The interface that hides everything between "ask the model for a completion" and "here's the assembled assistant message + usage." One implementation (`openAICompleter`) covers every provider; provider-specific deltas are switched on a `Provider` field.
_Avoid_: Client, LLM client, adapter.

**Round**:
One user message and every model turn that follows it until the model stops calling tools. A round may contain many turns.
_Avoid_: Conversation, exchange, request.

**Turn**:
A single model response within a round. The first turn of a round follows the user message; subsequent turns follow tool results.
_Avoid_: Step, iteration (those refer to `--max-steps`, the safety cap on turns per round).

**Reasoning effort**:
A request-time hint (`off`, `low`, `medium`, `high`) telling reasoning-capable models how much hidden chain-of-thought to spend. Sent on the wire as `reasoning: {effort: ...}`. Off means the field is omitted entirely.
_Avoid_: Thinking budget, CoT level.

**Reasoning policy**:
The agent-loop rule that decides which turns within a round get the configured reasoning effort. v1 policy: only the first turn after a user message reasons; tool-result follow-ups do not. Lives in the agent loop, not the completer.
_Avoid_: Reasoning mode, thinking strategy.

**Cost**:
Per-call USD spend, read from `usage.cost` on responses that carry it (currently OpenRouter only). Zero when the provider doesn't report it; lambda does not compute cost from a price table.

**Session**:
The conversation-side unit of an agent run: identity, history, context cache, lifecycle status. A Session points at a Workspace but isn't one — the conversation can outlive a given workspace (e.g. `/merge` rotates the workspace under the same session) and a workspace can outlive its conversation (suspend, resume later).
_Avoid_: Run, agent run, worktree (that's the Workspace).

**Workspace**:
The git-side unit of an agent run: an isolated worktree path, the branch created on it, the base branch and start SHA it was rooted at. Today's `worktree.Session` type — to be renamed. A Workspace is referenced by at most one active Session at a time.
_Avoid_: Worktree (overloaded with the git primitive), sandbox, checkout.

**Project context**:
User-authored guidance loaded from `AGENTS.md` (or `CLAUDE.md` as a fallback) and spliced into the system prompt. Discovered by walking up from cwd to the first `.git` ancestor. Disabled with `--no-project-context`. Distinct from skills (markdown packs loaded on demand) and from the `<environment>` block (cwd, OS, git status — derived, not authored).
_Avoid_: Memory, instructions, rules.

## Relationships

- A user message starts one **Round**; the **Round** contains one or more **Turns**.
- Each **Turn** is one call to a **Completer**.
- The **Reasoning policy** decides per-**Turn** whether to send the configured **Reasoning effort**.
- A **Provider** value selects request shaping and which API-key env var is read.
- A **Session** owns a sequence of **Rounds** and references one **Workspace** at a time.
