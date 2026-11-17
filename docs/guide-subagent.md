# seek 子代理 & Worktree / Subagent & Worktree

Subagents let the parent agent spawn child agents — each with its own session, token budget, and optionally an isolated git worktree — for parallel research, drafting, and experimentation.

> The parent agent can spawn subagents for parallel execution — isolated sessions, token budgets, and optional git worktrees. Only the summary crosses back, preserving prefix cache.

设计文档见 [`docs/prd/archive/feature-subagent.md`](prd/archive/feature-subagent.md)。

---

## 1. 快速开始 / Quick Start

```bash
# TUI: 让 agent 使用子代理并行探索
"调研三个方案：用 Subagent 分别评估 gorilla/webrpc/connect-go"

# agent 自动使用 agent 工具 spawn 子代理
→ [sub: spawning agents...]
  agent-1: gorilla/mux ✅ 成熟稳定
  agent-2: webrpc 📦 轻量但小众
  agent-3: connect-go 🔧 gRPC 兼容
```

---

## 2. 三种子代理类型 / Subagent Types

| Type | Tools | Workflow | Use case |
|------|-------|----------|----------|
| **general-purpose** | Full tools (minus agent/ask_user) | Inherits parent permissions | General sub-tasks |
| **explore** | Read-only (read/grep/list_dir/git/webfetch/think) | Plan-analyze | Parallel research |
| **plan** | Same read-only set as explore | Plan-analyze, output is numbered action plan | Structuring a plan |

### 2.1 自定义类型 / Custom Types（P2-a）

Put a Markdown file with YAML frontmatter in an `agents/` directory to define your own type (same frontmatter dialect as SKILL.md):

```
<project>/.seek/agents/<name>.md   # project-level, checked-in（优先）
~/.seek/agents/<name>.md           # user-level
```

```markdown
---
name: verifier                      # optional — falls back to the filename
description: Runs the test suite and reports failures.
tools: [read, grep, list_dir, git, bash]   # subset of the parent registry; omitted = full set
provider: glm                       # optional lane override (deepseek | glm)
model: glm-5.3                      # optional
effort: low                         # optional (low | medium | high)
workflow: plan-analyze              # optional, tighten-only
max_turns: 60                       # optional
---
You are a test verifier subagent. …（body = the type's role prompt）
```

Rules (validated at load; a bad file is skipped with a startup warning, never fatal):

- `description` and a non-empty body are **required**; `name` must be kebab-case.
- Same-name resolution: **project > user > config > builtin**（a project `explore.md` overrides the built-in `explore`; a config entry only re-routes a built-in — see [guide-config.md](guide-config.md)）.
- `tools` may never include `agent` or `ask_user`（hard parse error）; unknown tool names are checked at spawn time.
- `workflow` accepts only `plan-analyze` — loosening is a config error (permission tightening is monotonic by design).
- Files load **once at session start**; edits take effect on the next seek launch (no hot reload — tool-schema bytes freeze per process for the prompt cache).

Inspect what's defined with `/agents types`（lists project, user, config, and builtin types with their lane overrides and shadowing provenance）or from the shell with `seek agents list`. Custom types are spawnable through the `agent` tool（P2-b）. The `seek agents` CLI also scaffolds and validates definitions:

```bash
seek agents list                    # registry as the next session will load it
seek agents check                   # validate every definition file (non-zero exit on errors)
seek agents create verifier --description "Runs the test suite." \
  --tools read,grep,git --workflow plan-analyze   # scaffold ~/.seek/agents/verifier.md
seek agents create auditor --description "..." --project   # <cwd>/.seek/agents/ (checked in)
seek agents remove verifier         # delete a user-scope definition (project ones: git)
```

---

## 3. Worktree 隔离 / Worktree Isolation

When spawned with `isolation="worktree"`, a subagent works in a temporary git worktree:

- Worktrees live at `~/.seek/projects/<pid>/worktrees/<wt-id>/`
- Anchored by a git ref under `refs/seek/worktrees/<wt-id>` (outside the user's default refspec)
- Dirty-discard safety net: when discarded, content is stashed to `refs/seek/discarded/<ts>` before hard-reset (48-hour recovery window)
- Orphan self-healing: a session that dies BEFORE cleanup leaves no marker, so the age-based fallback (`--orphan-after`, default 7d) collects unmarked refs at startup/`gc` — but only with no unmerged commits (`merge-base --is-ancestor` vs HEAD) and a clean removable directory; anything dirty/locked/unmerged is skipped + logged for manual review
- GC via `seek worktree gc` (runs at startup and on demand)

```bash
seek worktree list  # Enumerate seek-managed worktrees on disk (prior sessions included)
seek worktree gc    # Garbage-collect stale worktrees (discarded refs 48h + orphans 7d)
```

---

## 4. 安全边界 / Safety

- **Permission monotonic-tighten** — a child's Pref × Workflow can only be tighter than the parent's. `Policy.Spawn(restriction)` enforces it at the type level.
- **Spawn depth = 1** — subagents cannot themselves spawn. Prevents fan-out runaway.
- **Cost rolled up** — `cache.Tracker.AdoptChild` wires the child's per-turn usage into the parent's cumulative cost.
- **ReadOnly dispatch marker** — the `agent` tool is marked ReadOnly so multiple `agent` calls in one assistant turn run concurrently, not sequentially.

---

## 5. 查看子代理 / Listing

```bash
# TUI
/agents        # List subagents: spawn time, type, status, turns, tokens, description
/agents types  # List defined agent types (project > user > builtin), with lane + shadowing info
/worktrees     # List seek-managed worktrees: id, branch, path
```

---

## 6. CLI 参考 / CLI Reference

```bash
# Typed-agent definitions (M-O2.3):
seek agents list                    # List the agent-type registry (project > user > config > builtin)
seek agents check                   # Validate every definition file; non-zero exit on errors
seek agents create <name> --description "..." [--project]   # Scaffold a definition
seek agents remove <name>           # Delete a user-scope definition (project types: git)

# Worktree management:
seek worktree list  # Enumerate seek-managed worktrees on disk (prior sessions included)
seek worktree gc    # Prune stale worktrees: discarded refs >48h (--older-than),
                    #   unmarked orphans >7d (--orphan-after; 0 disables)
```

Design docs: [`docs/prd/archive/feature-subagent.md`](prd/archive/feature-subagent.md).  
Code: `internal/subagent/` · `internal/agenttypes/` · `internal/agentsmgr/` · `internal/agentscli/` · `internal/worktree/` · `internal/tools/agent/`.
