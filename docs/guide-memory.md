# Memory System

seek has a three-tier memory system that persists knowledge across sessions. Unlike session history (which saves raw message logs), memory captures **curated decisions, preferences, and project conventions** so the agent doesn't start from zero every time.

## Architecture overview

```
L  Long-term / Soul     ~/.seek/soul.md
   Cross-project user preferences, thinking habits.
   Resident in system prompt (~500 token hard cap).
   Written by `seek -dream` → user review.
                        ↑ Dream (M → L distillation)
M  Mid-term / Project   ~/.seek/projects/<hash>/memory.jsonl
   Key decisions + rationale per project.
   Index (name + tagline) injected into every prompt;
   full content fetched on demand via `memory_recall`.
                        ↑ Distill (S → M)
S  Short-term / Session ~/.local/share/seek/sessions/<id>.jsonl
   Full message history (see guide-sessions.md).
   `/distill` at session end extracts reusable decisions into M.
```

## Tools (agent-invokable)

The agent can read/write M-layer memory through five tools:

| Tool | Purpose |
|---|---|
| `memory_observe(name, tagline, content, tags?)` | Save a new observation. Async V4-Flash dedup + value check before persisting. Overwrites by name; use `memory_amend` to append. |
| `memory_remember(name, tagline, content, tags?)` | Same as observe but requires inline user y/N confirmation per call. Use when the decision needs a human nod. |
| `memory_recall(name)` or `memory_recall(query=...)` | Fetch full content by name, or search entries by query substring (matches tagline + tags). Bumps recall count on name lookup (keeps decay-score from staling it). |
| `memory_amend(name, append_content)` | Append to an existing entry's rationale. Preserves original + timestamps new content. |
| `memory_archive(name, reason)` | Move an entry to the archive. Removed from active M-index but preserved in `archived.jsonl` for audit. |

### When to use each

- **Signal you discovered mid-conversation** → `memory_observe` (async, no user prompt)
- **Decision needing user confirmation** → `memory_remember` (inline y/N prompt)
- **Retrieve details of a known entry** → `memory_recall(name=...)`
- **Search across memory** → `memory_recall(query="...")`
- **Add evidence to an existing entry** → `memory_amend`
- **Remove outdated entry** → `memory_archive`

## User commands

### TUI slash commands

| Command | What it does |
|---|---|
| `/distill` | Thinking-mode-extract ≤3 project decisions from this session → user y/n/e review → M layer |
| `/memory list` | List all active M entries for current project |
| `/memory show <name>` | Show full content of one entry |
| `/memory search <query>` | Search entries by tagline/tags |
| `/memory archive <name> --reason "..."` | Archive a stale entry |

### CLI

```bash
# Cross-project preference induction (L layer)
seek -dream                  # read-only: show candidates without writing
seek -dream --write          # generate candidates AND append to ~/.seek/soul.md

# Memory introspection
seek memory list             # list active entries for the project at cwd
seek memory list --project <hash>  # specify a project by hash
seek memory show <name>
seek memory search <query>
seek memory archive <name> --reason "..."
```

## Storage locations

| Layer | Path | Format |
|---|---|---|
| L (Soul) | `~/.seek/soul.md` | Plain Markdown with frontmatter |
| M (Project) | `~/.seek/projects/<project-hash>/memory.jsonl` | JSONL, one entry per line |
| M Archive | `~/.seek/projects/<project-hash>/archived.jsonl` | JSONL, stale entries moved here |
| M Index | Injected into system prompt at session start | Generated from `memory.jsonl`, inactive entries excluded |

## Forgetting (decay-score GC)

M entries have a decay score that drops with time and low recall activity. The GC pass runs at session start:

- **Grace period**: first 7 days — score stays at 1.0
- **Staleness threshold**: score drops below 0.50 → entry goes `Stale=true`, excluded from M-index
- **Hard delete**: stale for ≥60 days → moved from `memory.jsonl` to `archived.jsonl`

This prevents M-index bloat while keeping truly unused entries recoverable.

## Design docs

For the full design rationale, PRD, and implementation details:
- [`docs/prd/v1.md`](./prd/v1.md) — L/M/S three-tier architecture, decay model, distillation pipeline
- [`docs/prd/feature-active-memory.md`](./prd/feature-active-memory.md) — planned proactive memory curation (design phase)
- [`docs/book/chapter-16.md`](./book/chapter-16.md) — in-depth book chapter on the memory subsystem
