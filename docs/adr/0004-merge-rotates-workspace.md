# /merge rotates the Workspace under a persistent Session

Typical version-control intuition treats merge as terminal: branch ends, work is integrated, you start something new. Coding-agent flows don't fit that shape — the dominant pattern is "ship this chunk, keep iterating," and forcing a fresh Session at every merge throws away the conversation context that motivated the merge in the first place. lambda's policy: `/merge` squashes the current Workspace's branch onto base, then rotates the Session to a fresh Workspace at the new HEAD; the Session and its history continue uninterrupted. Starting fresh is a separate, explicit verb (`/new`).

## Considered options

- **`/merge` closes the Session.** Honest, no magic. Rejected because the iterative loop is exactly when the user hits `/merge`, and forcing `/new` there means re-establishing every piece of context the model just earned.
- **User picks at merge time** (`/merge` vs `/merge --continue`). Explicit, but the default still has to be one or the other — and "rotate" is the dominant case by a wide margin. Adding the flag later is non-breaking; making the wrong default is.

## Consequences

- The Session's history is **cross-Workspace**. `history.jsonl` records "we were on Workspace W1 from event 42–87, now on W2." Replay-time logic must be aware that file paths in older tool results may not exist in the current Workspace — which is one of the reasons resume strips tool envelopes (decision 5 in `.scratch/sessions-redesign/01-decisions.md`).
- The Workspace id schema needs a rotation suffix (`.r2`, `.r3`, …) so `git worktree list` stays self-documenting across rotations.
- Rotation is a multi-step operation (squash → new worktree → manifest rewrite → old teardown). A startup `repair()` pass reconciles orphan dirs left by mid-rotation crashes; no journal field in the manifest. See decisions doc §9.
- After rotation the conversation has tool-result references to a file tree that just changed under it. Mitigated by stripping tool envelopes on replay; not solved in the general case (mid-Round rotation could still confuse the model). v1 accepts this; `/merge` is rare enough mid-Round that we'll see if it matters in practice.
