# Reason only on the first turn of a round

Reasoning-capable models (Anthropic extended thinking, OpenAI o-series, DeepSeek-R1, etc.) bill reasoning tokens and add latency, so naively enabling reasoning for every model turn in a coding-agent loop burns budget on routine tool-result follow-ups (read this file, grep that pattern) where the next action is mechanical. lambda's policy: when `--reasoning` is set, send the configured effort only on the first turn of each round (the turn that immediately follows a user message); subsequent turns within the same round send no reasoning. The planning turn is where reasoning pays off most reliably; tool-result turns rarely need it.

## Considered options

- **Every turn at the configured effort.** Simplest, most expensive. Rejected because a 10-step tool chain would 10x the reasoning bill for marginal gain.
- **Synthesis-aware heuristic** (reason on turn 1, plus any turn where the previous response had no tool calls). The "no tool calls" trigger fires almost exclusively when the user is correcting a model that gave up — marginal value, extra rule to explain.
- **Explicit `/think` and `/nothink` per-turn overrides.** Useful for the "synthesize across many tool results" case, but `/think` is redundant for the next-turn-after-user case (the default already reasons there) and the broader "reason on every turn this round" semantic adds surface area we can defer until a real user hits the gap.
- **Provider-side `auto`.** Not uniformly supported across OpenRouter's model catalog.

## Consequences

- The reasoning policy lives in the agent loop, not the `Completer`. The completer just translates whatever effort the loop hands it for a given turn.
- Long tool chains that genuinely need synthesis at turn N (e.g., "after grepping 5 files, decide which one is the bug") get no reasoning at the synthesis point. If users hit this in practice, add a `/think` override that means "reason on every turn this round" — the v1 design leaves room for it without breaking changes.
- Default `--reasoning off` preserves current behavior; nobody pays for reasoning until they opt in.
