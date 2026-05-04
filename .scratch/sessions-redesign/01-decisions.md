# Sessions redesign — decisions

Counterpart to `00-brief.md`. The brief framed the problem; this doc
locks in the answers reached during design discussion. Implementation
plan is still TBD — this is the contract any plan has to honor.

## 1. Session and Workspace are distinct concepts

A **Session** is the conversation-side unit: identity, history, lifecycle.
A **Workspace** is the git-side unit: an isolated worktree path, the
branch on it, the base branch and start SHA it was rooted at.

A Session points at a Workspace but is not one. The conversation can
outlive a given Workspace (rotation on `/merge`) and a Workspace can
outlive its conversation (suspend, resume later, or be abandoned).

Today's `worktree.Session` becomes `worktree.Workspace`. A new
`session.Session` type owns identity, history, and a reference to the
current Workspace.

Definitions are captured in `CONTEXT.md`.

## 2. Identity

Session id format: `lambda-<YYYYMMDD-HHMMSS>-<rand4>`. The 4-char random
suffix is always present (not collision-detected). Reasoning: oneshot
mode in subagent loops can spawn many Sessions per second; a sortable
human-readable timestamp plus a guaranteed tiebreaker beats either ULIDs
(opaque) or naked timestamps (collide).

Workspace id mirrors the Session id on first creation. On rotation
(`/merge`), the Workspace id gets a `.r2`, `.r3`, … suffix.

## 3. Oneshot Sessions are ephemeral

Oneshot mode does not write a manifest. No `.lambda/sessions/<id>/` is
created; history is in-memory only; the Workspace is torn down on exit
(or kept on dirty exit, same as today). Justification: persistence is
justified by resume/enumerate/suspend, none of which apply to a
subagent that runs to completion.

A future `--persist` flag in oneshot mode could opt in.

## 4. Lifecycle verbs

| Verb | Effect |
|---|---|
| `/new` | Fresh Session (new history) + new Workspace |
| `/merge` | Squash current Workspace onto base, rotate to fresh Workspace, keep Session and history |
| `/suspend` | Persist Session, leave Workspace as-is on disk, exit cleanly |
| `/resume <prefix>` | Load Session manifest + replay history, reattach to its Workspace |
| `/discard [<id>]` | Drop Session + Workspace + branch. No arg = current Session, then auto-`/new` |
| `/sessions` | List all Sessions on disk |
| `/title <text>` | Set or change human-readable title on current Session |
| `/model <name>` | Switch the model used for subsequent Rounds; persisted to manifest |

`/merge` is synchronous and refuses to start if the Workspace is dirty
(must commit or stash first; same rule as `git merge`). On conflict it
aborts cleanly, leaves the Session attached to the old Workspace, and
surfaces the conflict to the user.

`/resume <prefix>` works in-flight: while attached to Session A,
`/resume B` flushes A and attaches to B in the same process. Bare
`lambda` startup always opens a fresh Session; resume is opt-in via
`lambda --resume <prefix>`.

## 5. History format

On disk: full message log in `history.jsonl` — user messages, assistant
messages (with tool_use blocks), tool_result blocks. This is what the
model actually consumed; storing anything less makes "resume"
semantically different from "continue."

On replay: tool_use and tool_result blocks are **stripped**. The model
only sees user + assistant text. Justification: forces the model
(especially smaller local ones) to re-fetch current state instead of
being misled by stale tool results. A future `--full` flag on resume
could override.

Edge case: a user message like "ok do that" with no surrounding tool
context is unusual but not broken — assistant text typically narrates
its own findings, so the strip-tools transcript still reads coherently.

Redaction is a separate, deferred problem. The on-disk log contains
whatever tool output the model consumed.

## 6. Concurrency model

One active Session per process. Multi-session = multi-process. A
shared on-disk registry under `.lambda/sessions/<id>/` is enumerated by
directory scan. `/sessions` lists across processes; `/resume` either
attaches (suspended) or refuses (lockfile held by live PID).

Concurrency primitive: a `lock` file inside `.lambda/sessions/<id>/`
holds the attached process's PID. Stale locks (PID gone) are detected
and reclaimed on resume.

`os.Chdir` stays — there's still only one cwd needed per process.
The in-process multi-Session refactor (every tool taking an explicit
workspace arg) is explicitly punted; the brief's symptoms don't
require it.

## 7. Disk layout

```
.lambda/
├── sessions/
│   └── lambda-20260504-101500-a3f1/
│       ├── session.json     # manifest
│       ├── history.jsonl    # message log
│       └── lock             # PID of attached process
└── worktrees/
    └── lambda-20260504-101500-a3f1/   # current Workspace; rotates
```

Sibling, not nested. Nesting the Workspace inside the Session dir would
risk corrupting git's worktree registration on any rename and worsens
Windows path-length pressure.

The Workspace dir name carries the Session id so `git worktree list` is
self-documenting. After rotation: `lambda-20260504-101500-a3f1.r2/`.

The manifest references the Workspace by id; `session.Workspace().Path()`
resolves the path fresh each call. Path baking
(`.scratch/session-path-indirection/01`) is eliminated.

## 8. Manifest schema (`session.json`)

```json
{
  "id": "lambda-20260504-101500-a3f1",
  "version": 1,
  "created_at": "2026-05-04T10:15:00Z",
  "last_active_at": "2026-05-04T11:42:00Z",
  "title": null,
  "workspace_id": "lambda-20260504-101500-a3f1",
  "base_branch": "main",
  "base_start_sha": "abc123...",
  "model": "claude-opus-4-7",
  "provider": "openrouter"
}
```

No `status` field. Session existence is derived from the directory's
existence; active vs suspended is derived from the lockfile (held by a
live PID = active; absent or stale = suspended). `/discard` deletes the
directory, so "closed" needs no representation.

`base_branch` and `base_start_sha` rotate on `/merge` (the new
Workspace's base is the new HEAD). Pre-rotate base lives in git
history, not the manifest.

`model` is mutable; `/model <name>` writes back to the manifest.

`title` is nullable; set via `/title`.

Manifest writes are atomic: write tmp, fsync, rename.

## 9. `/merge` rotation atomicity

Rotation sequence:

1. Squash-merge current Workspace's branch onto base.
2. `git worktree add` the new Workspace dir + branch at the new base HEAD.
3. Atomically rewrite manifest (`workspace_id` updated).
4. Tear down old Workspace dir + delete old branch.

The manifest is the source of truth for "which Workspace is current."
Steps are ordered so that any crash leaves at most one orphan Workspace
dir under `.lambda/worktrees/`. No journal field; no `pending_op`.

On startup, a `repair()` pass reconciles:

- Manifest's `workspace_id` has no matching dir → hard error to user
  ("workspace missing; resume or discard?").
- Workspace dirs not referenced by any manifest → silently GC.

## 10. Suspend and resume corner cases

| Situation | Behavior |
|---|---|
| Suspend with dirty Workspace | Leave dirty as-is. Resume sees a dirty tree; model sees git status in environment block. |
| Resume, Workspace dir gone | Hard error with `/discard` hint. No silent recreation — that hides real data loss. |
| Resume, base branch advanced | One-line warning ("base advanced N commits since suspend"). No automatic rebase. |
| Resume, Workspace branch deleted externally | Recreate the branch at the Workspace's HEAD. The work is still there; the label is just missing. |

## 11. Migration

Clean break. lambda is pre-1.0; users running existing
`.lambda/worktrees/<ts>/` dirs from the old code path should merge or
discard via raw git before upgrading. New code ignores legacy dirs
(warn-and-proceed, not refuse-to-start). No `lambda migrate` command.

## 12. `/sessions` UI

```
ID                                  TITLE                      LAST ACTIVE       STATE
lambda-20260504-114200-b7c2  *      fix merge rotation         3 minutes ago     active
lambda-20260504-101500-a3f1         (untitled)                 2 hours ago       suspended
lambda-20260503-184530-d4e9         redesign sessions          yesterday         suspended
```

`*` marks the Session this process is attached to. State derived from
lockfile presence + PID liveness.

## Out of scope for v1

- Redaction of secrets in `history.jsonl`. Hard problem; deferred.
- In-process multi-Session (parallel agent loops sharing one process).
  Hard ceiling is `os.Chdir` being process-global. Punted.
- Forensic rotation history (`.lambda/sessions/<id>/workspaces/<n>/`).
  Squash commits on base are the audit trail.
- Tags / arbitrary metadata in the manifest. YAGNI.

## Related

- `.scratch/sessions-redesign/00-brief.md` — original framing
- `.scratch/session-path-indirection/issues/01-session-path-baked-at-startup.md`
  — closed as a side effect of decision 7 (path resolved fresh from
  Workspace id, not baked at startup)
- `internal/worktree/worktree.go` — type to be renamed
  `worktree.Workspace`; loses `OriginalCwd` field (irrelevant once
  Session is detached from any particular invocation)
