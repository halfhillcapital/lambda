# Skills

Skills are markdown files of instructions that the model can load on demand. The system prompt advertises a list of available skills (name + one-line description); when the model decides one is relevant, it calls the `skill` tool to pull in the body.

## Layout

A skill is a directory containing a `SKILL.md`:

```
~/.claude/skills/
└── grill-with-docs/
    ├── SKILL.md
    ├── CONTEXT-FORMAT.md
    └── ADR-FORMAT.md
```

`SKILL.md` has YAML frontmatter and a markdown body:

```md
---
name: grill-with-docs
description: Interview the user relentlessly about a plan until reaching shared understanding.
---

<instructions the model follows once the skill is loaded>
```

Sibling files (`CONTEXT-FORMAT.md` above) are referenced from the body and read with the normal `read` / `bash` tools. When a skill is loaded, lambda prepends `Base directory for this skill: <abs path>` to the body so the model knows where to find them.

## Where lambda looks

In order, project wins on name collision:

1. `./.claude/skills/` — repo-local, travels with the code
2. `~/.claude/skills/` — user-global, shared with Claude Code (see [ADR-0001](../adr/0001-share-claude-code-skills-dir.md))

Override with `LAMBDA_SKILLS_DIR` (comma-separated, prepended to the search path).

## Frontmatter

Parsed: `name` (required, must match directory name), `description` (required).

Ignored: everything else. `allowed-tools` triggers a one-time stderr warning — lambda does not enforce it.

A skill with malformed frontmatter or a missing required field is skipped on startup with a warning; one bad skill does not take down the agent.

## Invocation

Two paths:

- **Model-driven.** The model sees the listing in its system prompt and calls `skill(name="<skill>")` when it judges one relevant. The tool returns the body; the model proceeds.
- **User-driven.** Typing `/grill-with-docs <args>` in the REPL synthesizes a user message asking the model to run that skill. The model still loads the body via the `skill` tool — the slash command is just a shortcut. Built-in slash commands (`/new`, `/clear`, `/help`) win on name collision, with a startup warning. Unknown `/foo` keeps the existing "unknown command" error.

## Lifecycle

Skills are scanned once on startup. Editing a skill body mid-session takes effect on the next `skill` call (the body is re-read each time). Adding or removing a skill requires `/new` or a restart so the system-prompt listing refreshes.
