# seek 子代理 & Worktree / Subagent & Worktree

Subagents let the parent agent spawn child agents — each with its own session, token budget, and optionally an isolated git worktree — for parallel research, drafting, and experimentation.

> The parent agent can spawn subagents for parallel execution — isolated sessions, token budgets, and optional git worktrees. Only the summary crosses back, preserving prefix cache.

设计文档见 [`docs/prd/feature-subagent.md`](prd/feature-subagent.md)。

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

---

## 3. Worktree 隔离 / Worktree Isolation

When spawned with `isolation="worktree"`, a subagent works in a temporary git worktree:

- Worktrees live at `~/.seek/projects/<pid>/worktrees/<wt-id>/`
- Anchored by a git ref under `refs/seek/worktrees/<wt-id>` (outside the user's default refspec)
- Dirty-discard safety net: when discarded, content is stashed to `refs/seek/discarded/<ts>` before hard-reset (48-hour recovery window)
- GC via `seek worktree gc` (runs at startup and on demand)

```bash
seek worktree list  # Enumerate seek-managed worktrees on disk (prior sessions included)
seek worktree gc    # Garbage-collect stale worktrees
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
/agents    # List subagents: spawn time, type, status, turns, tokens, description
/worktrees # List seek-managed worktrees: id, branch, path
```

---

## 6. CLI 参考 / CLI Reference

```bash
# Subagent management is through the agent tool (TUI/chat), not CLI
# Worktree management:
seek worktree list  # Enumerate seek-managed worktrees on disk (prior sessions included)
seek worktree gc    # Prune stale worktrees (rescue-stash refs older than 48h)
```

Design docs: [`docs/prd/feature-subagent.md`](prd/feature-subagent.md).  
Code: `internal/subagent/` · `internal/worktree/` · `internal/tools/agent/`.
