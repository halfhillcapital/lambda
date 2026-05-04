# Sessions redesign — discussion brief

Status: discussion doc. No implementation plan yet — the goal here is to
agree on what a "session" is before we change anything.

## Where we are today

A "session" in lambda is implicit. There is no `Session` type that owns the
agent run end-to-end; the closest thing is `worktree.Session`
(internal/worktree/worktree.go:24), which only models the git-side state:

```
Enabled, Path, Branch, BaseBranch, StartSHA, RepoRoot, OriginalCwd
```

Everything else that logically belongs to "this run of the agent" lives in
disjoint places:

- conversation history → in-memory in the agent loop, not persisted
- tool registry cwd → captured at startup from `session.Path`
- process cwd (`os.Chdir`) → set once at startup
- TUI `rebuildSections` closure → captures `session.Path` by value
- context sections cache → in-memory in the TUI
- the on-disk worktree dir (`.lambda/worktrees/<ts>/`) → the only durable
  artifact, but it carries no agent state, only git state

There is no manifest, no session id beyond the timestamp embedded in the
branch name, and no lifecycle verbs beyond `Start` / `Finalize` / `Merge`.

## Symptoms this causes

1. **No resume.** Quitting the TUI loses history, context cache, and any
   in-flight reasoning. The worktree survives but the agent state doesn't.
2. **Path baked at startup** — see
   `.scratch/session-path-indirection/issues/01`. Three independent callers
   captured `session.Path`, so the worktree dir can't move; `/merge` has to
   reset-in-place instead of recreating the worktree.
3. **No multi-session story.** Running two agents against the same repo
   works only by accident (both call `Start`, both get distinct timestamps);
   there's no enumeration, no "switch to session X", no shared registry.
4. **`/merge` rotation is awkward.** Branch name rotates but path doesn't,
   so `git worktree list` shows a stale-looking timestamp on the path.
5. **Finalize is the only exit.** No "suspend" — keep the session, walk
   away, come back later — without leaving the user to read
   merge/discard hints out of stderr.

## What an explicit Session model would look like

A `Session` becomes the spine other subsystems consume:

- **Identity:** stable id (e.g. `lambda/<ts>` or a ulid), distinct from any
  particular branch name or path.
- **Manifest on disk:** `.lambda/sessions/<id>/session.json` with: id,
  created-at, base branch, start sha, current worktree path, current
  branch, history file pointer, status (active/suspended/merged/discarded).
- **History persistence:** conversation log written incrementally to
  `.lambda/sessions/<id>/history.jsonl` (or similar) so resume is just
  "reload the manifest + replay history into the agent."
- **Accessors, not captures:** `session.Path()` / `session.Cwd()` read
  fresh. Tools registry, agent loop, TUI all hold a `*Session`, not a
  string. Path baking goes away.
- **Lifecycle verbs:**
  - `Start` — new session
  - `Resume(id)` — load manifest + history, re-attach worktree
  - `Suspend` — flush state, leave worktree alone, exit cleanly
  - `Merge` — squash onto base, then either rotate or close (open
    question; see below)
  - `Discard` — tear down worktree + branch + manifest
- **Enumeration:** `List()` over `.lambda/sessions/`, surfaced via a
  `/sessions` command.

## Open questions to settle before any code

1. **Session id vs branch name.** Should the id be the branch name, or
   independent? Independent gives us free rotation; same-as-branch is
   simpler to debug. Probably independent.
2. **Merge ends the session, or rotates it?** Today merge rotates
   (resetting to base, fresh branch). With explicit sessions we could
   instead say merge closes the session and the user starts a new one. Less
   magic, but more friction for the "iterate, merge, iterate" loop.
3. **History format.** JSONL of agent events is the obvious default; the
   harder question is what's in each event (tool calls? full tool results?
   redacted?). Replay correctness depends on this.
4. **Concurrent sessions.** If we allow multiple, do they share a tool
   registry process or each get their own? `os.Chdir` being process-global
   makes the answer non-trivial.
5. **Suspend semantics.** Does suspend just persist state and exit, or does
   it also park the worktree somewhere (so the user's `git worktree list`
   isn't cluttered)? Probably the former — keep it simple.
6. **Migration.** Existing worktrees under `.lambda/worktrees/` predate
   any manifest. Do we adopt them on first run, or require a clean break?

## Tradeoffs / why this is bigger than it looks

- **Spine refactor:** tools, agent loop, TUI all currently work with raw
  strings (path, branch). Switching them to a `*Session` accessor touches
  every caller. The payoff is real but it's not a one-PR change.
- **History persistence is its own design problem.** Naive "log every
  event" hits redaction (secrets in tool output), size (long tool results),
  and replay-fidelity (model nondeterminism) issues. Worth a separate
  issue once the manifest piece is agreed.
- **`os.Chdir` is process-global.** Until we either remove the dependency
  on cwd in tools or accept "one active session per process," multi-session
  has a hard ceiling.

## Proposed next step

Pick **one** of:

a. Spike the manifest + lifecycle verbs (no resume yet) and migrate
   `worktree.Session` to be owned by the new type. Clears
   `.scratch/session-path-indirection/01` as a side effect.

b. Spike resume specifically, accepting today's path-baking, by adding
   history persistence + a manifest just rich enough to re-attach. Lets
   us prove out the persistence design before committing to the spine
   refactor.

(a) is the cleaner architecture; (b) is the faster user-visible win. They
converge eventually.

## Related

- `.scratch/session-path-indirection/issues/01-session-path-baked-at-startup.md`
- `internal/worktree/worktree.go` — current implicit session
- `internal/worktree/merge.go` — rotate-in-place, the most visible symptom
