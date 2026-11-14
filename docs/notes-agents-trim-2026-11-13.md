# AGENTS.md 缩词草案（2026-11-13，卡 c_7afk5i 产出，待人批）

> 卡片：butler `c_7afk5i` · 分支 `drive/c_7afk5i` · 基线 commit `97f8ee8`（= `origin/main`）
> **本卡不改 AGENTS.md**（改它须人批，见 `docs/prd/feature-slim-loop.md` §9）。下面六个块是可直接应用的缩word 动作，应用后跑 `scripts/verify-doc-budgets.sh` 验。

## 证据

`scripts/verify-doc-budgets.sh --report` 在基线 commit 上已红（**不是本卡引入**，`origin/main` 同值）：

```
FILE                WORDS  CEILING   TARGET   STATUS
AGENTS.md            3295     3200     1800   OVER by 95
CLAUDE.md            3084     3100     1800   frozen — 16w to ceiling, 1800 is the goal
```

`docs/budget.json`（字节面）两者都还 ok（AGENTS 22739/24000、CLAUDE 21256/23000）——红的是 `scripts/doc-budgets.tsv` 这条**词数**硬 gate（CI job `doc-budgets`）。CLAUDE.md 差 16 词到 ceiling，本次不动；若采纳下列改动且遵守 AGENTS↔CLAUDE 同步规则，对应段落可镜像同步。

## 缩法（只删复述，规则与指针全留）

六个块合计 **-119 词**（3295 → 3176，ceiling 3200 下方留 24 词）。所有被删细节都能在下方「信息去处」找到出处，故符合「文档减法 = 信息不丢（指针 / 归档 / 代码承载）」口径。

### 块 1 `## Tool usage workflow` 第 5 段（-25 词）

旧：

```markdown
Never page through a whole file to answer a question grep can answer — small files (≤ 32 KiB) come back whole regardless; the rule targets needless full reads of larger files. The prefix cache survives only when old messages are byte-identical; lazy whole-file reads balloon prompt tokens and degrade cache hit rate.
```

新：

```markdown
Never page a whole file to answer what grep can answer — larger-file reads balloon prompt tokens and break the byte-identical prefix cache; small files come whole anyway.
```

### 块 2 `## Tool descriptions` 的 pattern 四步（-23 词）

旧：

```markdown
Pattern (proven in `eval/cases/tool-selection/`; framework in `eval/README.md`):
1. **Eval case first** — `prompt.txt` + `expect.json` defining the desired behaviour in measurable terms.
2. **Baseline** — run and record to `eval/results/` before changing anything.
3. **Edit the description** — 10–20 words of targeted guidance; zero runtime cost, the string is already sent.
4. **Compare** — re-run; success is the tool-call sequence changing, not just binary PASS/FAIL.
```

新：

```markdown
Pattern (proven in `eval/cases/tool-selection/`; framework in `eval/README.md`): write the eval case first (`prompt.txt` + `expect.json`), record a baseline into `eval/results/`, edit the description (10–20 words of targeted guidance, zero runtime cost), re-run — success is the tool-call sequence changing, not just PASS/FAIL.
```

### 块 3 `## Token & prefix-cache` 第 4 条（-34 词）

旧：

```markdown
- **Anything that must appear in the prompt prefix for correctness but would otherwise vary is captured ONCE per session — never recomputed per turn.** The canonical example is the session date: it's injected as `sysprompt.Header.Date` (computed once at startup in `cmd/seek` and threaded through every `Compose` call plus the subagent Manager's *static* `SessionDate`), NOT via a `time.Now()` inside the assembler — `Compose` must stay a pure function of its `Header` (the `TestCompose_IsDeterministic` guard enforces this). A per-turn date would mutate the prefix on every request and crater the hit ratio. Refresh only at session boundaries (`-resume` = new process = one accepted bust). See `docs/pitfalls.md` "Today's date belongs in the system prompt".
```

新：

```markdown
- **Anything that must appear in the prompt prefix but would otherwise vary is captured ONCE per session — never recomputed per turn.** Canonical case: the session date (`sysprompt.Header.Date`, set in `cmd/seek`). It is not `time.Now()` inside the assembler — `Compose` stays a pure function of its `Header` (`TestCompose_IsDeterministic` enforces it); a per-turn date mutates the prefix every request and craters the hit ratio. Refresh only at session boundaries (`-resume`). See `docs/pitfalls.md` "Today's date belongs in the system prompt".
```

信息去处：`docs/pitfalls.md`「Today's date belongs in the system prompt」（`ManagerOpts.SessionDate` 静态传递、`-resume` 一次性 bust 的完整表述都在那里）。

### 块 4 `## Code conventions` 的 reconstruct 段末两句（-15 词）

旧：

```markdown
  Why it's a discipline and not a refactor: dsh gets this structurally — every model request is derived from its append-only log (`deriveMessages()`), so state *cannot* drift from the record. seek is not rewriting to that model; the cost/benefit doesn't hold for a single-binary local CLI. Applying the question per feature captures most of the value at none of the cost. See [`docs/dsh-analysis.md`](docs/dsh-analysis.md) §2 and §9.2.
```

新：

```markdown
  Why discipline, not refactor: dsh derives every request from its append-only log, so state *cannot* drift; seek isn't rewriting to that model — the cost/benefit doesn't hold for a single-binary local CLI. Asking the question per feature captures most of the value at none of the cost. See [`docs/dsh-analysis.md`](docs/dsh-analysis.md) §2, §9.2.
```

信息去处：`docs/dsh-analysis.md` §2 / §9.2（`deriveMessages()` 的机制原文在那里）。

### 块 5 `## Testing` 的 bar 段（-10 词）

旧：

```markdown
**The bar: test the failure modes, not just the happy path.** Happy-path-only suites give false confidence and have already let real bugs ship — commit `986a485` was an orphan-tool_calls regression that sat in `pkg/agent` under green CI for weeks because nothing exercised mid-stream cancellation. Every released feature ships with tests for, where applicable:
```

新：

```markdown
**The bar: test the failure modes, not just the happy path.** Green happy-path CI has already let real bugs ship — `986a485` (orphan tool_calls) sat in `pkg/agent` for weeks because nothing exercised mid-stream cancellation. Every released feature ships with tests for, where applicable:
```

信息去处：`docs/pitfalls.md` 986a485 条（含后续三条同源路径）。

### 块 6 `## Testing` 的豁免段（-12 词）

旧：

```markdown
If one of these doesn't apply, fine. If you skipped a test for one that does, say why in your final message (and in the PR description, if one is opened). "Real-API behaviour only" is valid; "didn't have time" is not.
```

新：

```markdown
If one doesn't apply, fine; if you skipped one that does, say why in the final message and PR. "Real-API behaviour only" is valid; "didn't have time" is not.
```

## 未删的候选（留给下一刀）

- `## Tool descriptions` 的 maintainer 前言与「tuning history」三行表：证据价值高，但可整段迁 `eval/README.md`（约 110 词），需人判「AGENTS.md 是否还该留 maintainer 指导」。
- `## AGENTS.md vs CLAUDE.md` 全节：可压到两句，但同步规则属结构段，应与 CLAUDE.md 同船改。

## 应用方式（人批后）

1. 逐块替换上述六处（纯文本替换，无结构变化）。
2. `scripts/verify-doc-budgets.sh` —— 期望 `AGENTS.md 3176 3200 1800 frozen — 24w to ceiling` 且 exit 0。
3. 块 3（Token & prefix-cache）与块 4（Code conventions）触及结构段——按 AGENTS↔CLAUDE 同步规则须同船镜像到 CLAUDE.md；CLAUDE.md 当前 3084/3100（只剩 16 词），镜像时须在 CLAUDE.md 里等量删词，改完复跑 `scripts/verify-doc-budgets.sh`。
4. commit 带 `Slim: AGENTS.md -119 words (六个复述块，指针全留)` trailer。
