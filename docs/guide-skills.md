# Skills

Skills are reusable instruction sets stored as Markdown files. The agent can fetch a skill's body on demand via the `Skill` tool, then follow its steps. Think of them as named workflows: "run Go tests", "do a dual-model review", etc.

## Search order

seek discovers skills from four locations, in priority order (lower number wins on name collision):

| Priority | Location | Notes |
|---|---|---|
| 1 | `<project>/.seek/skills/` | Project-local, seek-specific |
| 2 | `<project>/.claude/skills/` | Project-local, Claude Code compatible |
| 3 | `~/.config/seek/skills/` | User-global seek skills |
| 4 | `~/.claude/skills/` | User-global Claude Code compatible |
| — | Built-in | Bundled with seek; always available |

A project skill shadows a user skill of the same name. Built-ins are always lowest priority.

## Built-in skills

| Name | When to use |
|---|---|
| `dual-model` | Non-trivial multi-step tasks — plans first with V4 reasoning mode (`Thinking.Type=enabled, ReasoningEffort=high`), executes with regular chat, then reviews |
| `go-test-runner` | Running, debugging, or analyzing Go test failures |

## Writing a skill

Create a `.md` file in any of the search locations above. The file must start with YAML frontmatter:

```markdown
---
name: my-deploy
description: Deploy the service to staging. Use when asked to deploy, push to staging, or run the deploy script.
---

# Deploy workflow

1. Run `bash("make build")` to produce the binary.
2. Run `bash("make deploy-staging")` to push.
3. Confirm with `bash("curl https://staging.example.com/health")`.
```

- **`name`** — the identifier the agent uses in `Skill("my-deploy")`.
- **`description`** — sentence or two telling the agent *when* to use this skill. Write it like a trigger condition: "Use when…".

The body is plain Markdown. Keep it concise — it gets injected into the agent's context on every use.

## Invoking a skill

The agent picks up skills automatically based on the `description` field. You can also trigger one explicitly:

```
Use the dual-model skill to plan this refactor.
```

## Migrating from Claude Code

If you already have skills in `~/.claude/skills/` or `<project>/.claude/skills/`, seek loads them automatically — no migration needed. The frontmatter format (`name`, `description`) is the same.
