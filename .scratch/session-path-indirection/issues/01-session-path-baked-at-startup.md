# Session path is baked at startup in three places

## Problem

`session.Path` is captured independently by three callers at startup:

- `tools.Registry` — captures cwd when constructed
- `os.Chdir` — process-global, set once during session init
- `rebuildSections` closure in the TUI — captures the path by value

Because nothing reads the path through a single indirection, the worktree
directory on disk can't move after startup without invalidating all three.

## Where this bites

`internal/worktree/merge.go` `rotateBranch` (merge.go:167) is the visible
symptom. The natural implementation of `/merge` would be:

1. squash-merge the session branch onto base
2. delete the old worktree
3. create a fresh worktree at a new `.lambda/worktrees/<new-ts>/` path

Instead, we reset the existing worktree to base's tip and rename the branch
in place, reusing the original directory. Functionally equivalent, but:

- `git worktree list` shows a stale-looking path (timestamp from session
  start, not from the most recent rotation)
- the on-disk path no longer corresponds to the current branch name
- future features that want to move/recreate the worktree (e.g. base-branch
  switch, worktree relocation) will hit the same wall

## Proposed direction

Introduce a single `Session` (or `Workspace`) handle that owns the path, and
have `tools.Registry`, the agent loop, and `rebuildSections` read
`session.Path()` fresh on each use rather than capturing the string. Then
`rotateBranch` (or its successor) can legitimately tear down and recreate
the worktree.

## Tradeoff / why this isn't a one-liner

`os.Chdir` is process-global. Even with a Session handle, rotation has to
re-Chdir and we need to audit anything that holds a relative path
mid-flight:

- tool calls in progress that resolve paths against cwd
- log file handles
- anything spawning subprocesses with inherited cwd

So it's not pure plumbing — it's "define the lifecycle of cwd in this
process." Worth doing, but scope it deliberately.

## Acceptance

- `session.Path` is read through one accessor; no caller caches the string
- `/merge` (or a follow-up) can recreate the worktree at a new path without
  breaking the agent loop, tools, or TUI rendering
- documented behavior for cwd during rotation (either: rotation pauses tool
  execution, or tools are cwd-independent)
