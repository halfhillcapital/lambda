# One active Session per process

The architecturally pure answer to "how do you support multiple Sessions" is to decouple every tool from process cwd — every shell-out takes a `workspace` arg, `os.Chdir` goes away entirely, parallel agent loops share a process. That refactor touches every tool, every git helper, and every path-manipulation site in the codebase. lambda punts it: each lambda process attaches to exactly one Session at a time. Multi-Session UX (enumerate, resume any, discard any, run two agents against the same repo) is delivered via a shared on-disk registry under `.lambda/sessions/<id>/` plus a per-Session lockfile. Two TUI windows = two processes = two Sessions. What's not supported is one TUI driving two Sessions side-by-side.

## Considered options

- **Many Sessions per process, decoupled from cwd.** The pure answer. Rejected as a 6-month refactor whose payoff doesn't match any user need we can articulate today.
- **Hybrid: one interactive Session per process; subagent oneshots inline with explicit workspace args.** Effectively what we landed on for the oneshot path (subagents don't compete for `os.Chdir` because they're ephemeral and inherit it explicitly). Treated as an implementation detail of "one Session per process," not a separate architectural mode.

## Consequences

- `os.Chdir` stays. Tools continue to read process cwd. The "session path baked at startup" symptom (`.scratch/session-path-indirection/01`) is solved by making `session.Workspace().Path()` resolve fresh, not by removing the cwd dependency.
- Concurrency primitive: `lock` file inside `.lambda/sessions/<id>/` holds the attached process's PID. `/resume` against a Session whose lockfile points at a live PID refuses; against a stale lock (PID gone) it reclaims silently.
- `/sessions` enumerates by directory scan — works across processes for free, no IPC.
- Hard ceiling on parallel agent loops in one process. If we ever want that (e.g., a "run these 5 subtasks in parallel against shared repo state" workflow), the cwd refactor is the path forward and this ADR gets superseded.
