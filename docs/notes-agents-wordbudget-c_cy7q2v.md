# AGENTS.md 词预算超线 40 词处置——两案并列呈批（2026-11-21）

> 卡片：butler `c_cy7q2v`（承接 `c_6mbjfq` 悬项）· 分支 `drive/c_cy7q2v` · 基线 commit `f85209cc` · AGENTS.md blob `82adea57`
> **本卡不改 AGENTS.md。** 终稿 diff 在本文 §4，呈用户过目（check-agents 铁律）后由主会话落码合入。

## 1. 背景（超线事实）

- AGENTS.md 现状 **3240 词** > 3200 上限（`scripts/doc-budgets.tsv`），超 40。过线发生于 `c_oyp2qf` 合并（3184→3244），非单卡违规——`c_6mbjfq` 用户拍板另卡承接，即本卡。
- `verify-doc-budgets` 在 `scripts/selftest.sh` 排除表内（「超限即红是它的职责」`c_i2cyb7` 口径），故 CI/selftest 车道不拦文档超线——这道闸只在显式跑 `scripts/verify-doc-budgets.sh` 时红。
- 口径：`wc -w` 全文件（`verify-doc-budgets.sh` 同源），下文所有词数同口径。

## 2. 两案并列（推荐 A）

### A 案（推荐）：压缩 diff，−77 词 → 3163（裕量 37）

七把刀，全部是「文件内去重」或「指向既有文档的细则指针化」。**不新建任何承载文件、不整段搬家**——butler 仓 `c_3erttb` 瘦身的实证教训是搬家会把过期状态快照冻结进没人看的 docs；本卡只删冗余、只指向已存在的权威载体。逐条核证见 §3。

### B 案：调线一行 `scripts/doc-budgets.tsv`

```diff
-AGENTS.md	3200	1800
+AGENTS.md	3300	1800
```

**理由（正方）**：超线 40 词是 `c_oyp2qf`（LaneOf 架构演进）带来的合法增长，非失控膨胀；`doc-budgets.tsv` 的 ratchet 哲学是「超 target 即 FROZEN at current size」——3200 这条线本身在 3184 时代就该冻到 3184，说明线与体的历史错位是存在的。

**理由（反方，为何不推荐）**：`verify-doc-budgets.sh` 头注释写明调线「requires explicit justification」，而冻结的全部价值就是「adding a paragraph requires displacing one」的位移压力——调线 40 词等于宣布这次不付位移费。A 案已证明 −77 词可以零语义损失拿到，位移费付得起，无需动线。

## 3. A 案逐条核证（每刀：删了什么 / 为何零语义损失 / 核处）

| # | 位置 | 删/压的内容 | 零语义损失论证 | 机械复核 |
|---|---|---|---|---|
| 1 | Code conventions | 整条「Permission denials are tool results…full contract in "Permission model" above」 | 该条是**自认的指针复述**——完整契约在同文件「Permission model」节逐字保留（含 `ErrDenied`、verbatim 回喂、Hint 子句、禁绕过） | 压缩后 `grep -c ErrDenied AGENTS.md` ≥ 1（Permission model 节原文未动） |
| 2 | reconstruct 节 preApproved 例 | 「zero it on step complete/skip, on Esc, on /plan off, on every SetWorkflow transition」清单 → 「at every exit listed in "Permission model" above」 | 同一清理时机清单在 Permission model 节 `preApproved` 条**原文完整保留**，此处是跨段复述 | `grep -n "SetWorkflow.*transition" AGENTS.md` 命中 Permission model 节原句 |
| 3 | Tool usage 节末段 | 「prefix cache survives only when byte-identical; whole-file reads balloon tokens…」→ 指针「the cache cost is "Token & prefix-cache constraints", below」 | 机制解释（exact byte sequence、缓存代价）在 Token 节开篇**逐字级保留** | `grep -n "exact byte sequence" AGENTS.md` |
| 4 | Token 节 session-date 条 | 四个实现细节（subagent 静态 `SessionDate`、`time.Now()` 禁入 assembler、`TestCompose_IsDeterministic` guard、`-resume` = one accepted bust）→ 「Full anatomy (…): docs/pitfalls.md "Today's date…"」 | 细则在 `docs/pitfalls/deepseek-api/today-s-date-belongs-in-the-system-prompt-but-only-if-captured-once-per-session.md` **逐条逐字承载**（Why/Fix 段含全部四项，含 `ManagerOpts.SessionDate` static-not-closure 论证）；铁律句与主例主干保留 | `grep -n "SessionDate\|TestCompose" docs/pitfalls/deepseek-api/today-s-date-*.md` |
| 5 | skill 蒸馏段 | 存储细则（`DraftStore`、telemetry rows、`skill-distill-state.json` 全路径、dream-state 类比）→ 「Storage/write-path details: the PRD」 | `docs/prd/feature-skill-distill.md` 对这些标识符 **23 处命中**；铁律（auto path NEVER installs、只许 skillmgr/skillstats 写）原文未动 | `grep -c "DraftStore\|skill-distill-state" docs/prd/feature-skill-distill.md` |
| 6 | memory→skill bridge 段 | 安装语义（`<project>/.seek/skills/`、no `.install.json`、effective next session、⚡/🛠 图标）→ 「target dir and install semantics: the PRD」 | `docs/prd/feature-memory-skill-bridge.md` **8 处命中**；核心规则「按键即人闸、never-install 只约束 automatic path」原文保留 | `grep -c "install.json\|WriteProjectSkill" docs/prd/feature-memory-skill-bridge.md` |
| 7 | 状态标记四处 | `(M-SD1+M-SD2/SD3 implemented)`、`(M-B1–M-B5 shipped)`、`(2026-11-16, inspect-rpc IDE line closed, c_np5gzu)→(c_np5gzu)`、`write path landed M-O2.3:` | 实现进度快照不是行为规则；权威载体是各 PRD 的 status header 与 butler 卡片史（AGENTS 内快照会过期——`c_3erttb` 教训）。卡片锚保留一个即可核 | `git log --oneline -- docs/prd/feature-inspect-rpc.md`；butler `memory_search c_np5gzu` |

hunk 级词差（脚本数删行/增行词数）：hunk1（Architecture 段，刀 5/6/7）−16；hunk2（刀 3）−12；hunk3（刀 4）−24；hunk4（刀 1/2）−25。合计 **−77**。

## 4. wordcount 前后对照

```
前（f85209cc，blob 82adea57）：3240 词 —— OVER by 40（verify-doc-budgets.sh --report 实测）
后（本 patch 试切实测）      ：3163 词 —— frozen — 37w to ceiling（同一脚本实测）
```

试切在本 worktree 真改真量后已恢复原状（`git status` 干净），patch 经 `git apply --check` 验证可干净应用于 `f85209cc`。

## 5. 终稿 diff（呈用户过目——批后由主会话执行）

```diff
diff --git a/AGENTS.md b/AGENTS.md
index 82adea57..7b3cc3fa 100644
--- a/AGENTS.md
+++ b/AGENTS.md
@@ -26,11 +26,11 @@ If unsure, log it — but only for pitfalls from the change at hand; mid-task di
 
 - **DeepSeek-first, two-tier providers**. `pkg/deepseek` is a first-class client with DeepSeek-specific fields (cache metadata, FIM endpoint, reasoner content). `pkg/llm` is a thin generic interface for second-tier providers (Anthropic / OpenAI / Gemini; GLM via the `compatible` adapter + pinned bigmodel.cn endpoint, no FIM / no cache metadata — `docs/pitfalls.md`).
 - **`pkg/deepseek` must not import `pkg/llm`** (CI lint enforces this — see `.github/workflows/ci.yml`). The whole point of the split is that DeepSeek-specific optimisations don't get lowered into a generic interface.
-- **Model capabilities live in `internal/modelcap`, never in a provider package.** `modelcap.Vision(model)` / `modelcap.Files(model)` are the single authorities for "does this model accept image input / document input" (static tables for every lane's defaults + additive runtime grants installed by `internal/opencatalog` from the Go gateway's own `modalities.input`, same override shape as `pricing.SetModelRates` / `budget.SetLimits`). It used to be `deepseek.IsVisionModel`, a one-id allow-list — which is how every second-tier lane silently answered "no vision" and dropped pasted screenshots. A capability is not a provider fact: don't park the next one inside whichever package shipped it first. Same rule, second resident: **`modelcap.LaneOf(model)`** is the single authority for "which provider lane speaks this wire id" (web ContextMeter's lane chip, W5 c_oyp2qf) — resolved by id alone (Go pool = `pricing.OpenCodeGoModels` single-source, shared ids → first-tier lane per opencatalog's directLaneIDs semantics, second-tier defaults by prefix), because surfaces like `seek serve` only ever see the model id, never the child's `--provider`. Images traverse the second-tier path via `llm.Message.Images` (data URLs), encoded per adapter; text-only messages must keep their exact previous wire bytes (prefix cache). Documents mirror this (`llm.Message.Files`, PDF-only per each second-tier wire — the format set lives in `pkg/llm`); the gate is lane-split: `modelcap.Files` on second-tier lanes, while the DeepSeek lane's Files API is account-level, so any model id there takes a document.- **Skill subsystem (v2)**: read-only loader in `internal/skill` (Anthropic Agent Skills layout: `<dir>/SKILL.md` + frontmatter); install/uninstall/update in `internal/skillmgr`; call-stats JSONL in `internal/skillstats`; shared CLI/TUI dispatcher in `internal/skillcli`. Loader is the only path that runs at startup; everything else is on-demand. **Never** add filesystem writes under `~/.seek/skills/` outside `internal/skillmgr` and `internal/skillstats` — those are the only two packages allowed to mutate user-level skill state. Skill auto-distillation ([`docs/prd/feature-skill-distill.md`](docs/prd/feature-skill-distill.md), M-SD1+M-SD2/SD3 implemented): cognitive funnel + the M-SD2 automatic path (SessionStart background batch, `$SEEK_AUTO_SKILL_DISTILL`, default **off**) both live in `internal/skilldistill`; drafts land in `~/.seek/skills/.drafts/` (a dot dir the loader deliberately skips) via `skillmgr`'s DraftStore, telemetry rows via `skillstats` — the auto path's only direct write is the per-project cadence state under `~/.seek/projects/<pid>/skill-distill-state.json` (same class as memory's dream-state). Iron rule: the automatic path NEVER installs — installing a draft is a human action via the existing `seek skill install`. Memory→skill bridge ([`docs/prd/feature-memory-skill-bridge.md`](docs/prd/feature-memory-skill-bridge.md), M-B1–M-B5 shipped): `/distill` ⚡/🛠 review's `s` action installs the candidate as a **project-level** skill (`<project>/.seek/skills/`, no `.install.json`, effective next session) via `skillmgr.WriteProjectSkill` — the review keystroke is itself the gate (a human has read description/body in-modal), so the never-install iron rule still bounds only the automatic path.
-- **Typed-agent definitions (P2-a)**: read-only loader in `internal/agenttypes` (`~/.seek/agents/` + `<project>/.seek/agents/`, skill-layout precedent, loaded once at session start — no hot reload); the three built-in templates live as data in `agenttypes.Builtins()`, which `internal/subagent/templates.go` adapts. Same red line as skills: **no filesystem writes under `~/.seek/agents/` outside `internal/agentsmgr`** (write path landed M-O2.3: `seek agents create/remove`, validated via round-trip `agenttypes.Parse` before anything touches disk) — the loader has no write API by design. Frontmatter dialect is shared via `internal/frontmatter` (extracted from `internal/skill`). Scope: [`docs/prd/feature-orchestration.md`](docs/prd/feature-orchestration.md) §5.
+- **Model capabilities live in `internal/modelcap`, never in a provider package.** `modelcap.Vision(model)` / `modelcap.Files(model)` are the single authorities for "does this model accept image input / document input" (static tables for every lane's defaults + additive runtime grants installed by `internal/opencatalog` from the Go gateway's own `modalities.input`, same override shape as `pricing.SetModelRates` / `budget.SetLimits`). It used to be `deepseek.IsVisionModel`, a one-id allow-list — which is how every second-tier lane silently answered "no vision" and dropped pasted screenshots. A capability is not a provider fact: don't park the next one inside whichever package shipped it first. Same rule, second resident: **`modelcap.LaneOf(model)`** is the single authority for "which provider lane speaks this wire id" (web ContextMeter's lane chip, W5 c_oyp2qf) — resolved by id alone (Go pool = `pricing.OpenCodeGoModels` single-source, shared ids → first-tier lane per opencatalog's directLaneIDs semantics, second-tier defaults by prefix), because surfaces like `seek serve` only ever see the model id, never the child's `--provider`. Images traverse the second-tier path via `llm.Message.Images` (data URLs), encoded per adapter; text-only messages must keep their exact previous wire bytes (prefix cache). Documents mirror this (`llm.Message.Files`, PDF-only per each second-tier wire — the format set lives in `pkg/llm`); the gate is lane-split: `modelcap.Files` on second-tier lanes, while the DeepSeek lane's Files API is account-level, so any model id there takes a document.- **Skill subsystem (v2)**: read-only loader in `internal/skill` (Anthropic Agent Skills layout: `<dir>/SKILL.md` + frontmatter); install/uninstall/update in `internal/skillmgr`; call-stats JSONL in `internal/skillstats`; shared CLI/TUI dispatcher in `internal/skillcli`. Loader is the only path that runs at startup; everything else is on-demand. **Never** add filesystem writes under `~/.seek/skills/` outside `internal/skillmgr` and `internal/skillstats` — those are the only two packages allowed to mutate user-level skill state. Skill auto-distillation ([`docs/prd/feature-skill-distill.md`](docs/prd/feature-skill-distill.md)): cognitive funnel + the M-SD2 automatic path (SessionStart background batch, `$SEEK_AUTO_SKILL_DISTILL`, default **off**) both live in `internal/skilldistill`; drafts land in `~/.seek/skills/.drafts/` (a dot dir the loader deliberately skips); the auto path's only other direct write is per-project cadence state. Storage/write-path details (DraftStore, telemetry rows, state file): the PRD. Iron rule: the automatic path NEVER installs — installing a draft is a human action via the existing `seek skill install`. Memory→skill bridge ([`docs/prd/feature-memory-skill-bridge.md`](docs/prd/feature-memory-skill-bridge.md)): `/distill` review's `s` action installs the candidate as a **project-level** skill via `skillmgr.WriteProjectSkill` — the review keystroke is itself the human gate (a human has read description/body in-modal; target dir and install semantics: the PRD), so the never-install iron rule still bounds only the automatic path.
+- **Typed-agent definitions (P2-a)**: read-only loader in `internal/agenttypes` (`~/.seek/agents/` + `<project>/.seek/agents/`, skill-layout precedent, loaded once at session start — no hot reload); the three built-in templates live as data in `agenttypes.Builtins()`, which `internal/subagent/templates.go` adapts. Same red line as skills: **no filesystem writes under `~/.seek/agents/` outside `internal/agentsmgr`** (write path: `seek agents create/remove`, validated via round-trip `agenttypes.Parse` before anything touches disk) — the loader has no write API by design. Frontmatter dialect is shared via `internal/frontmatter` (extracted from `internal/skill`). Scope: [`docs/prd/feature-orchestration.md`](docs/prd/feature-orchestration.md) §5.
 - **Plan-mode subsystem (v2 + v2.x)**: confirmation-gated workflow `analyze → propose → execute → adjust → report`. Driven by the `propose` tool (`internal/tools/propose/`) and progress-tracked by the `plan` tool (`internal/tools/plan/`). Substate state machine lives in `permission.Policy` (Mode / preApproved flag) + agent mode reminder + TUI status bar; transcript event-sourcing reconstructs state on `seek -resume` (see `plan/reconstruct.go`). Plan-approval artifacts (write-once markdown snapshots) land in `~/.seek/projects/<id>/plans/` via `plan/artifact.go`. Full design + status table in [`docs/prd/feature-plan-mode.md`](docs/prd/feature-plan-mode.md).
 - **i18n (view layer only)**: catalogue in `internal/i18n` (`i18n.T`; every key in ALL catalogues — parity-tested). Human-facing prose only; LLM-visible strings + status bar stay English (prefix-cache bytes + width oracle). Language resolves once per session; `/lang` is the only runtime swap. Scope: [`docs/prd/feature-i18n.md`](docs/prd/feature-i18n.md).
-- **RPC surfaces — single data-plane authority, frozen stdio** (2026-11-16, inspect-rpc IDE line closed, c_np5gzu): read-only data-plane methods live ONLY in `internal/rpc`'s `Registry` (host: `seek serve` POST `/rpc`; implementations in `internal/rpc/methods/`) — new inspection methods go there, never duplicated elsewhere. `--rpc` stdio's three methods (`agent/prompt` / `agent/info` / `session/list`, `internal/rpc/server.go`) are a **frozen legacy control surface**: keep its own switch, never hang it on the registry, never add methods. IDE integration goes through ACP (`seek acp`, `internal/acp`) — see [`docs/prd/feature-inspect-rpc.md`](docs/prd/feature-inspect-rpc.md) status header.
+- **RPC surfaces — single data-plane authority, frozen stdio** (c_np5gzu): read-only data-plane methods live ONLY in `internal/rpc`'s `Registry` (host: `seek serve` POST `/rpc`; implementations in `internal/rpc/methods/`) — new inspection methods go there, never duplicated elsewhere. `--rpc` stdio's three methods (`agent/prompt` / `agent/info` / `session/list`, `internal/rpc/server.go`) are a **frozen legacy control surface**: keep its own switch, never hang it on the registry, never add methods. IDE integration goes through ACP (`seek acp`, `internal/acp`) — see [`docs/prd/feature-inspect-rpc.md`](docs/prd/feature-inspect-rpc.md) status header.
 - See [`docs/prd/`](docs/prd/) for the full PRD series: v0 initial, v1 Memory, v2 Skill lifecycle, plus standalone feature PRDs (`feature-plan-mode.md`, `feature-webfetch.md`, `feature-permission-refactor.md`, `feature-active-memory.md`, `feature-mcp-client.md`, …).
 
 ## Tool usage workflow (load-bearing)
@@ -42,7 +42,7 @@ When exploring code, follow this order — skipping steps costs tokens and break
 3. `read`'s `limit` defaults to 200 (values above error; files ≤ 32 KiB always come back whole).
 4. `grep` caps at 20 matches by default (`max_matches` can be raised, but rarely should be).
 
-Never page through a whole file to answer a question grep can answer — the rule targets needless full reads of larger files. The prefix cache survives only when old messages are byte-identical; lazy whole-file reads balloon prompt tokens and degrade the hit rate.
+Never page through a whole file to answer a question grep can answer — the rule targets needless full reads of larger files; the cache cost is "Token & prefix-cache constraints", below.
 
 5. **read before edit** — `read(offset=N, path=...)` the target lines first to capture the `old_string`'s **exact whitespace** (preserved byte-for-byte; never guess tab depth). One read costs less than the mismatched-`old_string` error-fix loop.
 6. **Git queries: one per git-tool call** — never chain, never bash read-only git; several queries = parallel calls. Rationale: `docs/test-plan-git-tool-shape.md`.
@@ -60,7 +60,7 @@ DeepSeek charges ~10× less per token on cache hits. The cache key is an **exact
 - **Never modify old messages before sending to the API** — even "compression" or "summarisation" of old tool results invalidates the cache and can cost more than sending the full history.
 - **Token optimisation happens at write-time** (tool output size limits), not at send-time (in-transit mutation).
 - Tool output limits are enforced inside the tool itself. Do not add a post-hoc filter layer in `pkg/agent` or the API path.
-- **Anything that must appear in the prompt prefix for correctness but would otherwise vary is captured ONCE per session — never recomputed per turn.** The canonical example is the session date: it's injected as `sysprompt.Header.Date` (computed once at startup in `cmd/seek` and threaded through every `Compose` call plus the subagent Manager's *static* `SessionDate`), NOT via a `time.Now()` inside the assembler — `Compose` must stay a pure function of its `Header` (the `TestCompose_IsDeterministic` guard enforces this). A per-turn date would crater the hit ratio. Refresh only at session boundaries (`-resume` = new process = one accepted bust). See `docs/pitfalls.md` "Today's date belongs in the system prompt".
+- **Anything that must appear in the prompt prefix for correctness but would otherwise vary is captured ONCE per session — never recomputed per turn.** Canonical example: the session date — `sysprompt.Header.Date`, computed once at startup in `cmd/seek` and threaded through every `Compose` call; `Compose` stays a pure function of its `Header`. A per-turn date would crater the hit ratio. Refresh only at session boundaries. Full anatomy (subagent `SessionDate`, the determinism guard, resume-bust accounting): `docs/pitfalls.md` "Today's date belongs in the system prompt".
 
 ## Permission model
 
@@ -91,11 +91,10 @@ Self-recovery first — "ask the user" is the LAST resort. Read the error (the h
 - **New tools** live at `internal/tools/<name>/` with a `New(...)` constructor. If the tool can mutate the filesystem or shell, inject a `*permission.Policy`. If the tool wants user choice (picker / approval), inject `*askuser.Policy`.
 - **Sink interfaces: don't break the main contract — add OPTIONAL sibling interfaces.** When a new feature needs more context flowing into a tool's Sink, do NOT add parameters to the existing Sink method (every fake / recording sink in tests breaks). Define a sibling interface (`type FooReporter interface { Foo() ... }`) and upcast at the call site: `if r, ok := sink.(FooReporter); ok { r.Foo() }`. The propose tool carries four such optional interfaces (`DuplicateChecker`, `ProgressReporter`, `ContextReceiver`, `ArtifactReporter`); none required a Sink signature change. See `docs/pitfalls.md` "Plan artifact write needs context BEFORE Sink.Approved fires".
 - **Wire-format strings are contracts when something parses them.** A tool result string like `[plan: approved]` looks like prose, but `internal/tools/plan/reconstruct.go` scans for that prefix verbatim to seed plan-state on resume. Once a marker has a parser, the marker is wire format — additions go AFTER the closing token (`[plan: approved] (auto-approve-per-step)`), never inside it (`[plan: approved batch]` would silently break reconstruction). When introducing a new variant of an already-parsed result, add a test that asserts on `HasPrefix(out, "<marker>")` so future variants can't slip past.
-- **Permission denials are tool results, not fatal errors** (`internal/permission.ErrDenied`) — never bypass the flow; full contract in "Permission model" above.
 - **Session format is JSONL** (`schema_version=2`, `.jsonl` extension): line 1 is the header (all scalar metadata), lines 2..N are one `deepseek.Message` per line. Legacy `.json` files (schema_version ≤ 1) are migrated on next `Save`. `loadMeta()` reads only line 1 — do not call `Load()` when you only need metadata.
 - **Reconstruct state from the transcript — parallel RUNTIME state counts, not just parallel files.** The transcript is the only durable source of truth. Anything derivable from it should be folded from it on demand, never mirrored. Two forms of the same mistake:
   - **A parallel state file.** Resist adding a sibling `.json` / `.md` that mirrors runtime state — you'll fight drift forever. Artifact files (`~/.seek/projects/<id>/plans/`) are the exception: write-once human-readable snapshots, NOT state.
-  - **A parallel runtime field.** Holding a fact in a struct *and* reconstructing it on resume gives you two sources that must be kept in step by hand. `permission.Policy`'s `preApproved` is the live example of the interest this charges: because nothing derives it, the code has to remember to zero it on step complete/skip, on Esc cancellation, on `/plan` off, and on *every* `SetWorkflow` transition. Each of those resets is a bug that was available.
+  - **A parallel runtime field.** Holding a fact in a struct *and* reconstructing it on resume gives you two sources that must be kept in step by hand. `permission.Policy`'s `preApproved` is the live example of the interest this charges: because nothing derives it, the code has to remember to zero it at every exit listed in "Permission model" above — each of those reset points is a bug that was available.
 
   **Default question when a feature needs new state: "can this be folded from the transcript?" — ask it before reaching for "add a field + a reconstruct function".** Plan state is the shape to copy: the `propose` args and `plan(start|complete|skip)` calls in the transcript *are* the state; `reconstruct.go` replays them and nothing mirrors them. Approval is the shape to avoid: runtime `Policy` state plus transcript reconstruction, hybrid, hand-synchronised.
 
```

### 落地步骤（主会话，用户批 A 案后）

1. 从本文 §5 代码块提取 diff（本文件的第二个 ```diff 块）：`awk '/^```diff$/{n++; f=(n==2); next} /^```$/{f=0; next} f' docs/notes-agents-wordbudget-c_cy7q2v.md > /tmp/agents-compress.patch`——提取结果已验证与起草时的 `git diff` 逐字节一致、`git apply --check` 在 `f85209cc` 通过；若 main 已前进，先在分支内 rebase/merge 后重跑 `git apply --check`。
2. `wc -w AGENTS.md` 应得 **3163**；`scripts/verify-doc-budgets.sh` 应输出 `frozen — 37w to ceiling`。
3. `go test ./...`（纯文档改动，预期全绿）；commit 主题建议 `docs(agents): -77w dedup/pointer-isation, back under the 3200 ceiling (c_cy7q2v)`。

### 批 B 案则

改 `scripts/doc-budgets.tsv` 那一行（§2），`scripts/verify-doc-budgets.sh` 应转 ok；AGENTS.md 不动。

## 6. 机制预算与架构页

- **不新增机制**：零新文件（本文档为一次性处置记录，属 notes 惯例）、零新脚本、零新闸。
- **架构页不适用**：纯词预算处置，不改架构事实。
