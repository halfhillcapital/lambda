# Share `~/.claude/skills/` with Claude Code

lambda loads skills from the same `~/.claude/skills/` directory that Claude Code uses, in addition to a project-local `./.claude/skills/`. We chose to share the directory rather than carve out a lambda-specific path (e.g. `~/.lambda/skills/`) so that a user's existing skill library is available with zero migration and so that skills authored for one tool are portable to the other. The cost is coupling: lambda's behavior depends on a directory whose convention is owned by another tool, and a user editing a skill for Claude Code reasons will see the change reflected in lambda too. We accept that — the shared format is the whole point.

## Consequences

- `LAMBDA_SKILLS_DIR` env override exists for users who want isolation or for tests.
- Project-local skills override user-global ones on name collision.
- We parse only `name` and `description` from frontmatter. Other fields (notably `allowed-tools`) are ignored, with a one-time stderr warning when first encountered, because honoring per-skill tool restrictions correctly is a non-trivial harness change and lambda's tool surface is small enough that the safety win is marginal. Skill authors who rely on `allowed-tools` being enforced will be misled if we silently ignore it; the warning surfaces that gap.
