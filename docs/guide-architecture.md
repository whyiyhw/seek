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
- **Workflow** (`None` / `PlanAnalyze` / `PlanExecute`) — the ceremony you've entered. "What am I doing." Three entrances: `/plan` (TUI), the propose-tool flow, and ACP `session/set_mode` (protocol lane: mode `plan` ⇄ `default`, between turns — `guide-zed.md` §5.3).

**Workflow trumps Preference where they conflict.** `PrefYolo + WorkflowPlanAnalyze` is still read-only — that's plan mode's raison d'être. The workflow is a user-chosen safety boundary; pref cannot override it.

Permission denials are **tool results, not errors** — the agent sees the denial message and asks the user (or reconsiders), rather than crashing the loop.

**One seam, three lanes.** Every dangerous action routes through `Policy.askFn`; lanes only differ in transport — TUI (`approvalCh` → askuser widget), print/web (`--approval-stdin` JSONL cards on stdin, driven by the web hub's `ApprovalRouter`), and ACP (`session/request_permission` agent→client JSON-RPC, `cmd/seek/acp_approval.go`). `allow_always` / `"a"` / the TUI's `[a] always: <kind>` all land on `Policy.SetAlwaysAllow`. No answer (EOF / disconnect / timeout / client `-32601`) denies — never a hang.

Code: `internal/permission/` · `cmd/seek/approval_stdin.go` · `cmd/seek/acp_approval.go` · see `docs/prd/feature-permission-refactor.md`.

---

## 3. Tool System / 工具系统

seek exposes tools to the LLM as JSON schema definitions. Current tools: `read`, `grep`, `list_dir`, `write`, `edit`, `bash`, `git`, `webfetch`, `ask_user`, `agent`, `enter_worktree`, `exit_worktree`, `monitor`, `schedule_wakeup`, `plan`, `propose`, `references`, `Skill`, `skill_fetch`, `skill_commit`, `memory_observe`, `memory_remember`, `memory_amend`, `memory_archive`, `memory_recall`.

**Two ways external tools join the same registry** — both through one MCP client stack (`pkg/mcp` stdio transport + handshake + `tools/list`, bridged by `internal/tools/mcptool`), never a second implementation: `~/.seek/mcp.json` at startup (tools named `<server>__<tool>`, live until process exit) and an ACP client's `session/new`/`session/load` `mcpServers` (named `mcp__<server>__<tool>`, reaped when the session's transport ends). Names are folded into the provider alphabet `^[a-zA-Z0-9_-]{1,64}$` on the injected path — MCP allows dots, model APIs do not. `internal/acp` stays transport-only (it does not import `internal/tools`); the wiring lives in `cmd/seek/acp_mcp.go`.

**Key constraint**: Tool schemas are `[]byte` constants at package level (built once at init, not at call time). This ensures identical bytes across turns → DeepSeek's prefix cache hits.

- **Failure is an event, never a process death** — a panicking tool degrades to a failed tool call (the model sees the error and retries); a panic in the prompt goroutine becomes an `ErrorEvent` carrying the stack instead of killing the process. `AgentEnd.Finish` carries the final turn's upstream `finish_reason`, so consumers classify *how* a round ended from a typed field rather than from notice text. On the ACP lane the two compose into one guarantee the editor can trust: every round emits exactly one `_seek_turn_stop` frame **before** its response, so "no frame and no response" means the process itself died ([`guide-zed.md`](guide-zed.md) §4.1).

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
- **Protocol surface** — the ACP lane drives the same workflow axis via the spec's session modes: `session/new` advertises `modes: {currentModeId, availableModes: [default, plan]}`, `session/set_mode` flips `default ⇄ plan` between turns (`plan` = PlanAnalyze, read-only), confirmed by a `current_mode_update` notification. `plan-execute` has no wire form — its gate (the propose approval picker) is TUI-only. Two-state behavior is pinned by a real-binary e2e (plan: write denied, no disk write; default: the same write lands). `guide-zed.md` §5.3.

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

PRD: [`docs/prd/archive/feature-eval-v2.md`](prd/archive/feature-eval-v2.md) · Guide: [`eval/README.md`](../eval/README.md).

---

## 11. Task Lifetime — detached from the seek process (v8 柱 V1)

D2's refined reading of "zero daemon" (v8 §3 I2) allows a **task's lifetime** to escape the seek process, and forbids a resident supervisor. Both halves are load-bearing:

- **Detached, not supervised** — `seek task run "<cmd>"` forks a wrapper (`seek task exec`, same binary) that `setsid`s into its own session and runs the command via `/bin/sh -c`, stdout+stderr appending to `~/.seek/tasks/<id>.log`. The task survives the seek process that started it; when it exits, the wrapper appends the exit fact to `~/.seek/tasks/<id>.jsonl`.
- **Nothing is resident** — `seek ps` / `seek attach` / `seek kill` each read that directory once and exit. No listening socket, no polling loop, no auto-start config.
- **Facts on disk, status computed** — the record holds facts (pid, start, exit code, killed); `running` / `exited` / `failed` / `killed` / `zombie` / `lost` is folded from them plus one live pid probe. `zombie` is surfaced honestly rather than rounded to `running`; `lost` means the process vanished with no exit fact (OOM, external SIGKILL).
- **Not bgjob** — `internal/bgjob` (the bash tool's `run_in_background` + `monitor`) stays session-scoped, in-memory, killed by `Shutdown`. The two do not share code: sharing would force bgjob to grow disk/cross-process fields it is deliberately free of. Use `seek task run` when the work must outlive the session.

Not in v1: session-state handover / resume-after-restart (the detach full semantics). Code: `internal/taskproc/` · `internal/taskcli/`. PRD: [`docs/prd/archive/feature-task-lifetime.md`](prd/archive/feature-task-lifetime.md).

---

## 12. Editor / ACP lane (v7 柱 P) / 编辑器车道

`seek acp` speaks the Agent Client Protocol over stdio so editors drive seek like any other agent (Zed today, paseo headless). Five design points that are easy to get wrong later:

- **Capabilities are declared, versions are negotiated.** `initialize` answers with `acp.NegotiateVersion(requested)` — the highest version seek speaks that is ≤ what the client asked for, never an echo — plus a capability object. A downgrade is the response itself (plus a stderr line); the client is the side that closes if it cannot speak the answer.
- **Capabilities are NOT model-gated.** `promptCapabilities.image` promises "an image content block is accepted", not "this model sees". Models swap mid-session (`session/set_config_option`), so a handshake-time declaration may never depend on the current model; the per-turn router decides each image's fate.
- **One image seam for three entries.** TUI clipboard paste, `@path` references and ACP image blocks all land on the same `visionRouter` (`cmd/seek/visioninput.go`) → native attach on a vision model, an in-band switch-model note otherwise. ACP adds only the decode step; the wire order of text and image blocks is preserved (`acp.PromptParams.Segments`), so a marker lands between the sentences that surround it.
- **N sessions share one machine, time-multiplexed at turn granularity** (P1-2). Per-session state — cwd anchor, the policy's session axes (workflow + always-allow grants), the history stack, the model lane — lives in the backend's session table; the agent, the tool registry and the process cwd are a shared register that each turn's activate/deactivate swaps under a single-slot turn gate (a channel, so a queued prompt cancels cleanly with its ctx). Sessions therefore share no mutable state — they share a register. Anything that mutates the agent / policy / process cwd outside a gated turn (or outside the between-turns busy window the transport enforces for `set_config_option` / `set_mode` / `session/load`) breaks that invariant; the gate plus the busy checks are the whole enforcement. One surface the gate cannot order: `session/new` runs inline in the read loop WITHOUT the gate (a new panel must not queue behind a running turn) and reads — the first branch even writes — the live session slot, so every cross-gate slot access (`NewSession`'s block, `saveTurn`'s writes, `activate`'s install, `deactivate`'s pull-back) takes the backend mutex on BOTH sides; a mutex only excludes when everyone participates, gate-held writers included. The gate serialises turns, it is not a memory-safety edge.
- **The context meter is a projection, not a table.** Each completed model turn emits one `usage_update` (`session/update`) with ACP's own `used`/`size`; the numbers are the LAST turn's prompt+completion against `budget.Limit(model)` with `budget.Classify`'s 0.60/0.75 — exactly what the TUI's `formatBudget` and the Web ContextMeter read. A cumulative token sum would inflate quadratically (every turn re-sends the history); a second threshold table would drift from the surfaces users already trust. Extras (`percent`/`severity`, the threshold-crossing alert frame) ride the spec's `_meta` namespace, never new top-level fields (`internal/acp/usage.go`, `guide-zed.md` §5.6).

Code: `internal/acp/` · `cmd/seek/acp_approval.go` · `cmd/seek/visioninput.go` · Guide: [`docs/guide-zed.md`](guide-zed.md).
