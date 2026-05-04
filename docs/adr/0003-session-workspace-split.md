# Session and Workspace are distinct concepts

Most agent-tool prior art bundles "the conversation" and "the git worktree" into a single object — today's `worktree.Session` is the git half, the conversation half is unnamed and unowned. This bundling makes lifecycle operations awkward: `/merge` rotates the git side but the conversation has no opinion on whether it survives, suspend/resume can't be expressed cleanly, and "fresh conversation, same dir" has no verb. lambda splits them: a **Session** owns identity, history, and lifecycle; a **Workspace** owns the worktree path, branch, and base SHA. A Session references a Workspace at any moment but isn't one — the conversation can outlive a given Workspace (rotation on `/merge`) and a Workspace can outlive its conversation (suspend, abandon).

## Considered options

- **One bundled Session.** Conversation + worktree as a single unit. Simple, but `/merge` rotation has to fake it (rotate the git side while pretending the Session is the same), and "abandon worktree, keep conversation" has no clean expression.
- **Session = conversation only; keep `worktree.Session` as-is.** The most incremental option. Rejected because the name collision (`session.Session` vs `worktree.Session`) is exactly the conflation the redesign is trying to escape; renaming to `worktree.Workspace` is a one-time cost that makes the spine readable.
- **Conversation owns Workspace as private state.** Same shape as the chosen split, but Workspace not exposed as a first-class noun. Rejected because tools, the TUI, and `/merge` all need to talk about the Workspace by name; making it private just forces every consumer to reach through `Session.workspace` accessors with no benefit.

## Consequences

- Spine refactor: every caller currently holding a `worktree.Session` (tools registry, agent loop, TUI `rebuildSections`, `main.go`) becomes a caller holding `*session.Session` and reading `session.Workspace().Path()` fresh. Path baking (`.scratch/session-path-indirection/01`) is eliminated as a side effect.
- `/merge` becomes "swap the Workspace under this Session," not "rotate the session, but not really." Branch and dir both rotate, no stale-timestamp paths in `git worktree list`.
- Two nouns in `CONTEXT.md` instead of one. Worth it: the design discussion immediately got sharper once the words existed.
