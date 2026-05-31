# Skills

Skills are reusable instruction sets stored as Markdown files. The agent can fetch a skill's body on demand via the `Skill` tool, then follow its steps. Think of them as named workflows: "run Go tests", "do a dual-model review", etc.

## Search order

seek discovers skills from three locations, in priority order (lower number wins on name collision):

| Priority | Location | Notes |
|---|---|---|
| 1 | `<project>/.seek/skills/` | Project-local, committed to git |
| 2 | `~/.seek/skills/` (or `$SEEK_HOME/skills`) | User-global |
| 3 | Built-in | Bundled with seek; always available |

A project skill shadows a user skill of the same name. Built-ins are always lowest priority.

> **v0 → v2 migration**: earlier versions also scanned `<project>/.claude/skills/` and `~/.claude/skills/` for Claude Code compatibility. The v2 loader no longer scans those paths. Use `seek skill install <path>` to migrate any skills still living there.

## Built-in skills

| Name | When to use |
|---|---|
| `code-review` | Structured diff review — checks current working-tree changes for correctness bugs and cleanup opportunities. Quick (precision-first, blocking only) or thorough (exhaustive). `/code-review` (alias `/review`) |
| `dual-model` | Non-trivial multi-step tasks — plans first with V4 reasoning mode (`Thinking.Type=enabled, ReasoningEffort=high`), executes with regular chat, then reviews |
| `go-test-runner` | Running, debugging, or analyzing Go test failures |
| `plan-mode` | Explains the analyze → propose → execute → report loop. Auto-read when the mode reminder says `plan-analyze` or `plan-execute`, or user types `/plan` |

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

## Directory-package format (v2)

Since seek v0.3, a skill can be a **directory package** (`<name>/SKILL.md` + frontmatter), aligned with the Anthropic Agent Skills format. This enables extra metadata (version, license, allowed tools) and bundling of reference files:

```
my-skill/
├── SKILL.md          # skill body with frontmatter (name, description, version, etc.)
├── references/       # optional — reference docs the skill can cite
├── examples/         # optional — usage examples
└── scripts/          # optional — helper scripts (executed via `bash` tool)
```

Single-file `.md` skills continue to work as before. A directory package takes priority over a single file with the same name.

## Managing skills (CLI)

Seek provides a full lifecycle CLI under `seek skill` (also available in the TUI via `/skill <verb>`):

| Command | Description |
|---|---|
| `seek skill create <name>` | Scaffold a new skill package with `SKILL.md` template |
| `seek skill install <path>` | Install from local path, Git URL, or HTTPS tarball (`--project` for project scope, shared via git) |
| `seek skill list` | List all loaded skills with source and version |
| `seek skill status <name>` | Show detailed info for one skill |
| `seek skill stats` | Show usage statistics (call count, last used, etc.) |
| `seek skill update <name>` | Re-pull a skill from its install source |
| `seek skill uninstall <name>` | Remove a skill |

> **Migrating from Claude Code**: Single-file `.md` skills from `~/.claude/skills/` or `<project>/.claude/skills/` use the same frontmatter format and work after `seek skill install <path>`. The v2 loader no longer auto-scans `.claude` directories, nor does it scan `~/.config/seek/skills/`. Move any skills from those old paths to `~/.seek/skills/` (or `$SEEK_HOME/skills` if you use that override).

## AI-driven install (model-invokable)

Since v0.3.x, the agent (the AI model you're chatting with) can also install skills for you, using two special tools:

1. **`skill_fetch(source)`** — the agent fetches a skill package from a local path, Git URL, or HTTPS tarball and stages it under `/tmp/` for your inspection. The agent can read the skill's `SKILL.md` and any bundled files so you can judge whether it's legitimate before committing.
2. **`skill_commit(staging, name, source, scope)`** — after you've reviewed the staged content and chosen a **scope** (see below), the agent finalises the install. The system asks for your interactive approval before touching the filesystem.

### Scope: user vs project

When installing, you'll be asked to pick a scope — it's your call:

| Scope | Location | Available in | Shared via git |
|---|---|---|---|
| **user** | `~/.seek/skills/<name>/` | Every seek session on this machine | No (private to you) |
| **project** | `<cwd>/.seek/skills/<name>/` | Anyone who clones this repo | Yes (committed to git) |

The agent cannot guess this — it must ask. After install, the new skill is on disk but won't be available until you run `/new` (or restart seek), because the skill manifest is loaded at startup.
