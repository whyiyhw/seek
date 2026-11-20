# seek 设计与架构 / Architecture & Design

How seek works under the hood — seek 的底层工作原理。

---

## 1. Three-Tier Memory (L / M / S) / 三层记忆

seek doesn't start from zero every session. Three layers of persistence capture knowledge at different timescales:

- **Soul (L)** — `~/.seek/soul.md`. Cross-project user preferences and thinking habits. ~500 token hard cap, resident in system prompt. Written by `seek -dream` → user review.
- **Project Memory (M)** — `<project>/.seek/memory.jsonl`. Project-level decisions (why JSONL? which test pattern?). Accessed via `memory_observe` / `memory_recall`. Auto-indexed into system prompt.
- **Session (S)** — `<session-dir>/` on disk. Full turn history, checkpoint blobs, skill stats. Live only for the session duration; auto-cleaned on session end (except persistent checkpoints).

Design docs: [`docs/prd/archive/v1.md`](prd/archive/v1.md) (L/M/S three-tier architecture).  
Guide: [`docs/guide-memory.md`](guide-memory.md).  
Code: `internal/memory/` · `internal/memorycli/` · `cmd/seek/main.go` (dream path).

---

## 2. Permission System (Pref × Workflow, two axes) / 权限系统

Permission lives on two orthogonal axes — never crammed into a single enum:

- **Preference** (`Deny` / `Ask` / `Yolo`) — your standing posture. "How strict am I." Chosen at startup (`--yolo`, `--no-perm`) or toggled via `/yolo`.
- **Workflow** (`None` / `PlanAnalyze` / `PlanExecute`) — the ceremony you've entered. "What am I doing." Toggled via `/plan` and the propose-tool flow.

**Workflow trumps Preference where they conflict.** `PrefYolo + WorkflowPlanAnalyze` is still read-only — that's plan mode's raison d'être. The workflow is a user-chosen safety boundary; pref cannot override it.

Permission denials are **tool results, not errors** — the agent sees the denial message and asks the user (or reconsiders), rather than crashing the loop.

Code: `internal/permission/` · see `docs/prd/feature-permission-refactor.md`.

---

## 3. Tool System / 工具系统

seek exposes tools to the LLM as JSON schema definitions. Current tools: `read`, `grep`, `list_dir`, `write`, `edit`, `bash`, `git`, `webfetch`, `think`, `fim_complete`, `ask_user`, `agent`, `enter_worktree`, `exit_worktree`, `monitor`, `schedule_wakeup`, `plan`, `propose`, `references`, `Skill`, `skill_fetch`, `skill_commit`, `memory_observe`, `memory_remember`, `memory_amend`, `memory_archive`, `memory_recall`.

**Key constraint**: Tool schemas are `[]byte` constants at package level (built once at init, not at call time). This ensures identical bytes across turns → DeepSeek's prefix cache hits.

Code: `internal/tools/` · each tool in its own package with a `New(...)` constructor.

---

## 4. Prefix Cache Optimization / 前缀缓存优化

DeepSeek charges ~10× less per token on cache hits. The cache key is an **exact byte sequence** of the entire prior message history. This drives several architecture decisions:

- **System prompt is immutable** within a session — changing it invalidates the prefix cache for the entire history.
- **Never modify old messages before sending** — even "compression" or "summarisation" invalidates the cache and costs more than sending the full history.
- **Tool output limits enforced inside the tool itself** — no post-hoc filter layer in `pkg/agent`.

---

## 5. Multi-Provider Layer / 多 Provider 层

seek is DeepSeek-first but provider-agnostic. The architecture splits into two tiers:

- **`pkg/deepseek`** — first-class client with DeepSeek-specific fields: cache metadata, FIM endpoint, reasoner content. **Zero external dependencies.**
- **`pkg/llm`** — thin generic interface for Anthropic, OpenAI, Gemini, and OpenAI-compatible endpoints.

**Critical rule (CI-enforced)**: `pkg/deepseek` must not import `pkg/llm`. DeepSeek-specific optimisations must never be lowered into a generic interface.

Code: `pkg/deepseek/` · `pkg/llm/` · `pkg/agent/` (type switch between tiers).

---

## 6. Session Persistence / 会话持久化

Sessions are stored as JSONL files (`schema_version=3`):
- **Line 1** — header with all scalar metadata (model, timestamps, token counts). `loadMeta()` reads only this line.
- **Lines 2..N** — one `deepseek.Message` per line. v3 adds an optional `predicted_next` field on assistant messages (v4 柱 D suggested-reply); v2 readers ignore it via Go's default unknown-field handling.

Guide: [`docs/guide-sessions.md`](guide-sessions.md).

---

## 7. Plan Mode v2 — confirmation-gated workflow

`/plan` enables confirmation-gated mode. The model analyzes context, proposes a problem definition + completion conditions + step list via the `propose` tool, waits for user **approve / adjust / cancel**, then enters the EXECUTE substate with per-step pre-approval via the `plan` tool.

- **State machine via transcript** — substate reconstructed on `seek -resume` by replaying `propose` args + `plan` events. No parallel state file.
- **`done_when` is required** — a `propose` call without completion conditions is rejected with a recovery error, so "what counts as done" is a field rather than prose in the problem statement. `reviewed_by` (optional) names who checks the result from a source that did not produce it.
- **Plan artifact** — every approve writes a read-only markdown snapshot to `~/.seek/projects/<id>/plans/`, with `## Done when` rendered as a checkbox list between Problem and Steps.
- **approve_batch** — opt-in fast-path for auto-approve-per-step.

Code: `internal/tools/propose/` · `internal/tools/plan/` · `docs/prd/feature-plan-mode.md`.

---

## 8. Subagent & Worktree (v5 柱 G)

The parent agent can spawn subagents — each with its own session, token budget, and optionally an isolated git worktree. Only the summary crosses back, so the parent's prefix cache is not invalidated.

- **Permission monotonic-tighten** — a child's Pref × Workflow can only be tighter than the parent's.
- **Cost rolled up** — child's per-turn usage is accumulated into the parent's cumulative cost.
- **Spawn depth = 1** — subagents cannot themselves spawn.
- **ReadOnly dispatch marker** — the `agent` tool is marked ReadOnly so multiple calls in one turn run concurrently.

Code: `internal/subagent/` · `internal/worktree/` · `internal/tools/agent/`.

---

## 9. Cron / Wakeup / Triggers (v5 柱 H)

seek runs **no resident daemon**. Scheduling delegates to the OS (launchd / systemd / cron / Task Scheduler), which invokes `seek cron tick` once a minute.

- **Three input sources** — scheduled jobs (`seek cron create`), model self-scheduled wakeups (`schedule_wakeup` tool), external triggers (JSON files in `~/.seek/cron/triggers/`).
- **Explicit env injection** — opt-in `~/.seek/cron/env` overlay file (dotenv format).

Guide: [`docs/guide-cron.md`](guide-cron.md).  
PRD: `docs/prd/archive/feature-routines.md`.

---

## 10. Eval Lane — judgement is a distribution, not a sample (eval v2 E1)

`eval/run.sh` is the behaviour-comparison lane (not CI, not a unit test): it pipes a case's `prompt.txt` through a real `seek -json` run and checks the event stream against `expect.json` bounds. Because the model is stochastic, a single sample cannot tell a regression from noise — so the judgement is a pass **rate** over N rounds:

- **`--rounds N`** (default 1 = historical single-sample behaviour, byte for byte). Each round is a full case lifecycle appending its own row with `round: k`; with N > 1 an aggregate row (`type:"aggregate"`, deliberately **no** `pass` key so `scripts/eval-trend.sh` cannot double-count it) records the verdict.
- **`expect.json` gate config** — `min_pass_rate` (`0 < m <= 1`, default 1.0, INCLUSIVE `>=` on IEEE doubles: 3/5 vs 0.6 passes, 0.67 does *not* equal 2/3) and `record_only: true` (recorded in its own column, never red, never green). Config lives in `expect.json` rather than a separate calibration table: one file already owns a case's judgement, and a second would be a parallel source of truth keyed by name.
- **Crashed rounds count in the denominator** (a run with no `agent_end` fails any `exact_agent_ends`-class bound); no separate error class yet.

Run it on demand — it is **not a resident mechanism**: no daemon, no CI job, no new startup path. Cost is per invocation (~$0.02–0.05 per case per round); unattended lanes run fake-key by rule ([`docs/prd/v8.md`](prd/v8.md) §9.1). Mechanism-budget answer: nothing new is resident, and the path this retires is manual multi-run re-review (the 2026-11-19 three-reds-two-false-alarms triage), now taken over by the aggregate verdict.

**No butler-sensitive path is touched.** The lane writes only `eval/results/*.jsonl` (checked-in data), the gitignored `eval/tmp/`, and — because `SEEK_HOME` is pinned to `eval/tmp/seek-home` — seek's own user-data writes land in that sandbox instead of the real `~/.seek`. It never writes `~/.local/bin`, `~/.agents`, or `~/Library/LaunchAgents`, and it makes no butler MCP calls. Mechanism self-tests: `scripts/eval-rounds-test.sh` (rounds/gate/record-only/malformed-input) and `scripts/eval-ablate-test.sh` (no-flag byte equivalence), both auto-discovered by `scripts/selftest.sh`.

PRD: [`docs/prd/feature-eval-v2.md`](prd/feature-eval-v2.md) · Guide: [`eval/README.md`](../eval/README.md).
