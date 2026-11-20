# pitfalls 台账分流 triage（2026-11-20 复核版，卡 c_yhpfsp）

> **一次性快照，非常驻机制。** 首版产出在 `777b4ff`（锚 `docs/pitfalls.md` @ blob `d9dd365`、HEAD `38149dc`，267 live）。本轮**不重做分流**，只做「对照分支现状逐条自查 + 如实写回」：267 条旧行的去向按标题逐一继承，补齐合并后新增的 3 条，锚点从旧 blob 行号**改锚到当前文件行号**，自查发现的偏差逐条记在 §7。
>
> 数据面：`grep -c '^### '` = 271（含 1 条格式模板）→ **270 live**；`grep -c '^### \[已吸收\]'` = 0；`bash scripts/pitfall-to-eval.sh --list` = 270 条。

## 0. 结论速览（验收三数，均可机械复核）

| 项 | 值 | 复核法 |
|---|---:|---|
| **live（分流后）** | **270** | `bash scripts/pitfall-to-eval.sh --list \| grep -cE '^ *[0-9]+  '` → 270 |
| **absorbed** | **0 条硬候补**（另有 2 条边缘候补留下次 archive pass 人判，§4） | 逐条按台账 :27-33 标记规则复查「主题是否被取代且仍会走偏」 |
| **修复卡候选（本卡不开卡）** | **5** | §2，每条带本轮实测证据 + 当前行号 |

其余去向：eval 已覆盖 **13** · eval 候选（未孵化）**2** · 留观 **250**。§5 表内逐行统计与上表同源：

```
FC 5 · EC 2 · EV 13 · W1 234 · W1n 5 · W2 9 · W3 2  = 270
留观 = 270 − 5(FC) − 2(EC) − 13(EV) = 250 = 234 + 5 + 9 + 2  ✓
```

## 1. 数据面、落盘选型与幂等

排序产出：`scripts/pitfall-to-eval.sh --list` → 270 条，score = 模型行为可能性启发式（标题 / Fix / Lesson 的 behavioural 信号加权和）。四条去向的判据：

| 去向 | 含义 | 判据 |
|---|---|---|
| **FC** 修复卡候选 | 坑描述的修复至今未做 / 被显式推迟 | 逐条对源码复核「Fix 是否真的落地」，未落地者进 §2 |
| **EC** eval 候选 | 模型行为坑，且 `eval/cases/` 无既有 case | `--list` 高分 + 无同名/同主题 case，走既有管道 `--make` 孵化 |
| **EV** eval 已覆盖 | 模型行为坑，case 已存在 | §3.1 映射表（不重复孵化） |
| **W1 / W1n / W2 / W3** 留观 | 修复已落地 / 修复已落地且有遗留注记 / 设计取舍·观察记录 / 环境·纪律 | 词条 Fix 带 commit / 测试 / 文件锚点、教训仍立 = W1（台账 :31 的「live 正常形态」），表内此行注记为 `—` 不再重复 |

**为什么落独立文件 `docs/pitfalls-triage.md` 而非台账内注记**（选型沿用首版，理由本轮复核仍成立）：

1. `pitfalls.md` 是每会话被 agent 读的活文档（append-only 记新坑；已 434 KB，超 `docs/budget.json` 预算 107 KB），逐条插入注记会让体量再近翻倍，且与既有「Status: live vs absorbed」节职责重叠；
2. 本仓纪律（AGENTS.md「Reconstruct state from the transcript」）：**一次性分析快照不是常驻状态**，不镜像进活文档——快照落独立文件、标注源与生成口径，下轮 triage / archive pass 整体重生成；
3. absorbed 的正式标记（`### [已吸收]` + 尾注）与搬移是 **archive pass 的职责**（规则在台账 :27-33）；本 triage 只提候选，**不改任何词条正文**。

**幂等（验收③）**：本文件是**整体覆盖写生成物**，唯一可变区是 §5 表（`<!-- triage-table:start -->` 与 `<!-- triage-table:end -->` 之间）。生成器读两个输入：当前 `docs/pitfalls.md`（行号 + 标题 + score）+ **本文件上一次的表**（按标题继承去向与注记）。因此重跑：不追加、不产生第二条注记、不重复计票；同一份 `pitfalls.md` 重跑 → 表逐字节相同（§6 有实测命令与结果）。台账新增词条时生成器**故意失败并列出未分流标题**，逼一次人判，而不是悄悄给默认去向。

**机制预算（验收④）**：triage 是**一次性动作，非常驻机制**——不新增 `scripts/` 脚本、不进 CI/selftest、无定时任务、无 `docs/budget.json` 条目；生成器是一次性 python + 一个 awk 打分片段（打分逻辑与 `scripts/pitfall-to-eval.sh` 的 `parse_entries` 同源，复制而非抽象），用后即弃，口径记在 §6。**架构页同步义务（验收⑤）：不适用**——本卡只做台账治理 + 文档写回，不触 butler 常驻设施、部署形态或任何运行机制。

### 1.1 台账自洽复核（本轮实测，五数互证）

| 口径 | 读数 | 结论 |
|---|---:|---|
| `grep -c '^### '` − 模板 1 | 270 | 机械计数 |
| 分布表 42 行逐行合计 | 270 | ✓ |
| 分布表 `**Total**` 单元格 | 270 | ✓ |
| 四桶 rollup（TUI 34 · API 36 · Go 43 · 其余 157） | 270 | ✓ |
| `scripts/pitfall-to-eval.sh --list` 行数 | 270 | ✓ |

卡面所引「255 live」是**首版之前**那张表的过期 Total（表内 42 行实合 263、Total 手写 255，本就差 8）。该表已由首版（`777b4ff`，255→267）与 main 侧随后两刀（267→268→269→270）逐次修正到位；本轮复核**无残差，故不动任何数值**。CHANGELOG 原文（`CHANGELOG.md:12`）的「249 条、关闭 0 条」是 v0.12.1 写作时读数，同属历史计数，不是当前错误。

## 2. 修复卡候选清单（5 张，供后续开卡直接引用；本卡不开卡）

行号 = **当前 `docs/pitfalls.md`** 的词条起始行；证据列 = 本轮（2026-11-20）逐条 `grep`/`read` 实测。

| # | 行 | 主题（建议卡名起点） | 现状证据（本轮实测） |
|---|---|---|---|
| FC-1 | :989 | 无头车道 askFn 接线（print 模式 `propose` / `ask_user` 恒 `ErrNoCallback`） | **仍成立**：`cmd/seek/main.go:1742` `return runPrint(...)` 早于 :1759 `policy.SetAskFn` 与 :1783 `askPolicy.SetAskFn`（两者都在其后的 TUI 块内）；`runPrint` 函数体（:2069 起）内无 `SetAskFn`。词条自记「not fixed here (out of card scope)」。开卡方向：auto-deny + 「plan printed, rerun interactively」契约，或 stdin 回调 |
| FC-2 | :1165 | thinking 工件 token 的 post-write 过滤 / CI 闸 | **仍成立**：`grep -rn 'end▁of▁thinking' --include=*.go --include=*.sh --include=*.yml` = **0 命中**（只有 `docs/pitfalls*.md` 提到）；词条 Fix 的两句（audit 写工具做 post-strip / 加 CI grep 闸）均未执行 |
| FC-3 | :1815 | `skillmgr.Uninstall` 名字闸（同族暴露） | **仍成立**：`internal/skillmgr/skillmgr.go:478` `Uninstall` 直接把 `opts.Name` 拼进 `filepath.Join(userDir, …)` 后 `Stat` / `RemoveAll`，无 `ValidName` + `filepath.Rel` 闸；对照 `internal/agentsmgr/agentsmgr.go:163` 已有 `agenttypes.ValidName` 闸（兄弟同卡已修）。词条明记 sibling NOT fixed |
| FC-4 | :2038 | Files API 按页计费恒 $0 | **仍成立**：上传响应无 `page_count`；`internal/fileroute/fileroute.go:158` `pricing.ParseCost(fo.PageCount)` 恒等于 `ParseCost(0)`，而 `internal/pricing/pricing.go:390` 对 `pages<=0` 直接返回 0。词条留给「F5 价格核对卡」定夺：本地数页（仅 PDF 可行）或等 API 补字段 |
| FC-5 | :555 | FIM 比率指标设计（v1.1 遗留） | **低优先，维持 deferred**：验收判据已由 PRD §6 降为绝对次数（`cmd/seek/benchmark.go:51` `FIMMinCalls`，取值见 :60 / :66），比率仅作可见性读数；开卡前提是拿到真实用量数据 |

## 3. eval 去向

### 3.1 已覆盖（13 条，case 已存在——不重复孵化）

| 行 | 主题 | 既有 case（本轮逐个 `ls eval/cases/` 复核存在） |
|---|---|---|
| :390 | 拒绝是纯税 → 自动修 | `git-subcommand-midtask` / `git-subcommand-shape` |
| :775 | grep 大 pattern 卡死 agent | `grep-refine-not-flood` |
| :843 | 小模型 byte-mismatch 烧轮次 | `edit-unicode-read-first` |
| :974 | GLM 车道契约族 | `glm-lane-contract` |
| :1179 | `limit` 参数面（曾 advertised 但不存在） | `read-whole-file` / `read-tail-fact` / `read-eof-blind` |
| :1422 | git 重复子命令 | `git-subcommand-shape`（auto-fix 已在库：`TestExecute_DuplicateSubcommandArgAutoFixed`） |
| :1471 | write 陈旧覆盖 | `write-guard-fallback` |
| :1499 | elided read 不担保全文件 | `write-elided-edit-fallback` |
| :1598 | 提示层禁令 vs 工具描述（`--list` 榜首 22 分） | `git-multi-query-no-bash`（`bash_chains` / `bash_git_calls` 指标） |
| :1612 | 拒绝 → 自恢复（Hint 语义） | `print-deny-semantics` |
| :1619 | eval 提词耦合工具上限 | `tool-selection` / `bash-overreach` |
| :1626 | `list_dir` 默认深度边界 | `bash-overreach`（重校后同族） |
| :1713 | 面向模型的 id 形态 | `session-id-contract` |

`--list` 的高分位 22 / 19 / 15 / 14 / 13 都能在上表找到对应 case —— 高分模型行为坑在 eval 侧的覆盖是实的。

### 3.2 候选（2 条，未孵化——走既有 `scripts/pitfall-to-eval.sh --make <line>`）

| # | 行 | score | case 草图 |
|---|---|---:|---|
| EC-1 | :1235 | 19（榜眼） | `skill-commit-scope-ask`：给「把这个 skill 装上」类任务，量测模型是否**先 `ask_user` 问 scope**（user / project）再调 `skill_commit`，而非默默选边。工具描述已写死「REQUIRED: ASK THE USER FIRST」（`internal/tools/skillinstall/skillinstall.go:159` / `:170`），这条 case 量的正是「描述能不能管住行为」 |
| EC-2 | :2062 | 0 | 拒绝文案 = 指令面：收到带处置指引的 deny 后，模型照 Hint 换形 / 停手，而不是绕字符串匹配（v0.12.1 推送绕闸事故的模型侧回归）。构造需注意车道：闸在 autopilot / goal 车道，eval 需造 reachable 的等价 deny 面 |

`eval/cases/` 现 30 个目录，`grep -l 'scope-ask\|deny-hint'` = 0 —— 两条确实未孵化。

## 4. absorbed 候补（0 硬 / 2 边缘）

- **硬候补 0**：按台账标记规则（「跟随今天会走偏」才算 absorbed）复查全部 270 条，未发现主题已被取代且仍会误导的词条。与 2026-11-19 archive pass 的「0 absorbed」**独立得出同一结论**，互证。
- **边缘 2 条**（表内 W1n，处置权归下次 archive pass 人判）：
  - `:575` `deepseek-reasoner` 无 `Thinking` 参数时静默降级——别名 2026-07-24 已死、`ModelChat` / `ModelReasoner` 常量已清扫（全仓 grep 0 命中），但 `ShouldEnableThinking` 的建议仍正确、不误导；
  - `:582` legacy 别名日落——「切默认」动作已执行完毕，词条属已执行历史。
  - 若判吸收：按规则搬 `pitfalls-archive.md` 时保留方法论指向——「同一日落类事故再发怎么办」活在 `:625`（别名日落静默孤儿引用）与 `:646`（retired id 仍应答、客户端价卡漂移）。

## 5. 全量逐条表（270 行，验收①②）

表列：当前行号 · 节 · **完整标题**（不截断——标题是去向继承的键，截断会让下一轮重跑失去锚）· `--list` score · 去向 · 注。**W1 行**注记统一为 `—`（= 修复已落地、教训仍立，台账 :31 的 live 正常形态；W1 中带遗留注记的仍逐条写明）；**非 W1 行**逐条写明理由。

<!-- triage-table:start -->
| 行 | 节 | 标题 | 分 | 去向 | 注 |
|---|---|---|---:|---|---|
| 90 | Filesystem / concurrency | Two parallel writers of the SAME content-addressed blob race on a shared `.tmp` filename | 1 | W1 | — |
| 97 | Filesystem / concurrency | SSE unsubscribe defer deleted the WRONG subscriber — `f.next` read at defer-run time, not defer-schedule time | 1 | W1 | — |
| 104 | Filesystem / concurrency | Topic fanout keyed coarser than the producer unit — cross-project SSE leak | 0 | W1n | 修复已进 main：webfrontend_topic.go topicFollow 实证（c_3736yl rework 已并） |
| 113 | Hook / memory | OnSessionStart must reset snapshot state for --resume correctness | 5 | W1 | — |
| 120 | Hook / memory | ObserveEnqueue accept counter races with non-blocking ResultChan send | 0 | W1 | — |
| 127 | Hook / memory | Shell-hook SkipReason needs per-event handling, not a single funnel | 1 | W1 | — |
| 134 | Hook / memory | Trust prompt must run pre-bubbletea — askuser channel isn't drained yet | 2 | W1 | — |
| 141 | Hook / memory | limitedWriter needs a pointer receiver — value receiver loses byte count | 1 | W1 | — |
| 148 | Hook / memory | A hook registered on the shared Registry fires in EVERY agent that shares it — including subagent children | 4 | W1 | — |
| 155 | Hook / memory | dropMalformed must list every Config event slice — Validate sees all, drop saw five | 2 | W1 | — |
| 164 | Skill distill (auto path) | Same session re-entering the distill window double-counted recurrence | 0 | W1 | — |
| 173 | TUI / terminal | A light palette derived by darkening a dark palette stops the "muted" role from receding | 0 | W1 | — |
| 180 | TUI / terminal | Status-bar glyph-width oracle mismatch soft-wraps the frame and ghosts the welcome banner | 1 | W1 | — |
| 187 | TUI / terminal | Explicit `Accept-Encoding: gzip` header disables Go's auto-decompression | 2 | W1 | — |
| 194 | TUI / terminal | "starting seek …" placeholder stuck for seconds (or forever) | 0 | W1 | — |
| 201 | TUI / terminal | Mouse drag-to-select did nothing | 0 | W1 | — |
| 208 | TUI / terminal | Long streaming replies freeze the screen — inline renderer can't cursor-up past a screen height | 1 | W1 | — |
| 215 | TUI / terminal | Garbage `]11;rgb:fae0/fae0/fae0\[1;1R` in the input box | 0 | W1 | — |
| 222 | TUI / terminal | lipgloss v2 background probe hangs forever when stdin is piped (Windows CONIN$) | 0 | W1 | — |
| 229 | TUI / terminal | Alt-screen mode breaks scrollback, copy, and content persistence | 1 | W1 | — |
| 236 | TUI / terminal | Inline-mode drift was self-inflicted by floor-pin padding | 2 | W1 | — |
| 245 | TUI / terminal | Reserved popup zone was the same mistake as floor-pin padding, wearing a different hat | 0 | W1 | — |
| 252 | TUI / terminal | Esc-cancelled stream silently dropped the user's prompt | 3 | W1 | — |
| 259 | TUI / terminal | `/model` updated the display but not the Agent's model — API calls still used the old model | 3 | W1 | — |
| 266 | TUI / terminal | Esc-cancel prompt restore can leak a prior chat prompt into the API-key / branch-name field | 3 | W1 | — |
| 273 | TUI / terminal | Agent.Messages() races with the Prompt goroutine when called from TUI (Ctrl+C, streamEnd) | 3 | W1 | — |
| 280 | TUI / terminal | /clear (cmdNew) swaps session/tracker before checking RebuildAgent error, leaving half-mutated state | 1 | W1 | — |
| 287 | TUI / terminal | replay.go skipped RoleTool messages and raw-rendered Markdown — --resume scrollback diverged from live | 0 | W1 | — |
| 294 | TUI / terminal | `initialSizeCmd` clobbers `teatest.WithInitialTermSize` in test environments | 3 | W1 | — |
| 301 | TUI / terminal | `tm.Output()` is a single-use reader — sequential `WaitFor` calls drain it | 3 | W1 | — |
| 308 | TUI / terminal | Ctrl+J newline inserts via `InsertString("\n")` broke cursor/viewport scrolling | 0 | W1 | — |
| 315 | TUI / terminal | Adding a builtin skill is prefix-cache-safe ONLY because the manifest is deterministically ordered | 6 | W1 | — |
| 322 | TUI / terminal | Batch ask_user picker: only the first option could be selected (transient faux pendingQuestion leaked past the router) | 0 | W1 | — |
| 329 | TUI / terminal | Windows Terminal forwards the IME-commit Enter — the 50ms paste guard turned Chinese messages into newlines | 0 | W1 | — |
| 336 | TUI / terminal | Shift+Enter is indistinguishable from Enter — bubbletea drops the modifiers on VK_RETURN | 3 | W1n | v2 迁移已解决（词条内嵌 RESOLVED 注记）；lesson「框架大版本后重验平台事实」仍通用 |
| 344 | TUI / terminal | Bubble Tea v2 migration: KeyMsg became an interface, paste became PasteMsg, teatest still pins v1 | 0 | W1 | — |
| 351 | TUI / terminal | Ctrl+V image paste on Windows was a silent no-op — a missing platform branch with zero feedback | 2 | W1 | — |
| 358 | TUI / terminal | PowerShell `-Command`: everything after it is command TEXT — never pass a path (or any data) on that argv | 1 | W1 | — |
| 365 | TUI / terminal | Windows terminals swallow Ctrl+V — the key binding was unreachable no matter how good the grabber was | 0 | W1 | — |
| 372 | TUI / terminal | Bare `m.appendHistory(...)` as a statement drops the line — same disease as `_ = tea.Println` | 4 | W1 | — |
| 383 | Agent loop | Static prompt prose can name tools the request's tools array won't carry — by design | 6 | W1 | — |
| 390 | Agent loop | A refusal the model does not learn from is a pure tax — auto-fix unambiguous intent instead | 13 | EV | 既有：git-subcommand-midtask / git-subcommand-shape |
| 397 | Agent loop | Esc mid-stream poisoned the session: orphan `tool_calls` rejected on every subsequent turn | 5 | W1 | — |
| 404 | Agent loop | Followup: ctx-cancel was one of FOUR paths to the same orphan state | 4 | W1 | — |
| 411 | Agent loop | Symlinks inside CWD let `write`/`edit` escape the CWD gate | 0 | W2 | 文档化限制：单用户本地工具威胁模型接受，测试钉死现状 |
| 418 | Agent loop | Symlinked CWD made every RELATIVE `write` compare "outside" — the escape fix over-tightened | 4 | W1 | — |
| 425 | Agent loop | `Policy.mode` was raced by /yolo flips against concurrent `Check` | 0 | W1 | — |
| 432 | Agent loop | `tools.ReadOnlyTool` is the on-switch for parallel dispatch — forgetting it makes "spawn 3 agents at once" run serial | 3 | W1 | — |
| 439 | Agent loop | `seek -upgrade` returned raw "permission denied" — users couldn't see they needed sudo | 0 | W1 | — |
| 446 | Agent loop | Tick's "len(due) == 0 → early return" silently skipped triggers + GC for the entire tick | 2 | W1 | — |
| 453 | Agent loop | OS scheduler invokes `seek cron tick` with a near-empty env — interactive-shell secrets invisible | 0 | W1 | — |
| 460 | Agent loop | `cache.Tracker.AdoptChild` had no resume-double-count guard — informal contract was load-bearing | 1 | W1 | — |
| 467 | Agent loop | `json.Unmarshal` silently drops unknown fields — LLM typos produce useless errors | 6 | W1 | — |
| 474 | Agent loop | A clean empty completion aborted the turn — but a retry needs "nothing was rendered" as its gate | 7 | W1 | — |
| 481 | Agent loop | Startup validation of custom agent types must run AFTER the registry is final | 0 | W1 | — |
| 488 | Agent loop | agenttypes.Definition zero-Origin reads as a built-in — hand-built Sets silently lose their types | 0 | W1 | — |
| 495 | Agent loop | `finish_reason:"refusal"` with empty content is not an "empty response" — and its marker must stream as a delta | 2 | W1 | — |
| 502 | Agent loop | /compact's own rescue call cannot fit an overflowed context — the recovery path was the deadlock | 5 | W1 | 修复已落地（复核）：`pkg/agent/compact_rescue.go` + `compact_rescue_test.go`（16 个 Test，含 `TestSummarise_OverflowRescue_Completes` / `TestSummarise_NormalPath_BytesUnchanged`） |
| 511 | DeepSeek API | Server web tools (`deepseek_read_url`/`deepseek_web_search`) have NO working coexistence shape with function tools — the 200 silently strips them | 3 | W2 | 判红关闭的决策记录（PRD §2.2），无修复意图 |
| 518 | DeepSeek API | `reasoning_content` rule for V4 thinking is the OPPOSITE of the old reasoner — conditional on tool_calls | 5 | W1 | — |
| 528 | DeepSeek API | Prefix cache hits are best-effort and short prompts don't trigger | 3 | W2 | best-effort 缓存行为已文档化，无代码可修 |
| 534 | DeepSeek API | Empty tool result + `omitempty` on `Message.Content` → "missing field `content`" from DeepSeek | 3 | W1 | — |
| 541 | DeepSeek API | `reasoning_effort` values are `high\|max` on DeepSeek V4 — not OpenAI's `low\|medium\|high` | 0 | W1 | — |
| 548 | DeepSeek API | FIM endpoint is at `/beta/completions` with legacy OpenAI shape | 4 | W1 | — |
| 555 | DeepSeek API | FIM usage ratio is inherently low — model prefers edit/write over fim_complete | 6 | FC | FIM 比率指标设计 deferred v1.1（见 §2 FC-5） |
| 562 | DeepSeek API | Streamed tool calls arrive as deltas keyed by `index` | 0 | W1 | — |
| 568 | DeepSeek API | "Respond with JSON only" instructions to the reasoner are unreliable | 4 | W1 | — |
| 575 | DeepSeek API | `deepseek-reasoner` without `Thinking` parameter silently behaves like fast-chat | 2 | W1n | 边缘 absorbed 候补（§4）：别名已死常量已扫，但建议仍不误导——下次 pass 人判 |
| 582 | DeepSeek API | DeepSeek legacy aliases `deepseek-chat` / `deepseek-reasoner` sunset 2026-07-24 | 2 | W1n | 边缘 absorbed 候补（§4）：日落已执行完毕，词条属已执行历史——下次 pass 人判 |
| 589 | DeepSeek API | `<project>/.seek/project-id` is per-machine state — must be gitignored | 0 | W1 | — |
| 596 | DeepSeek API | PrePromptHook output must be byte-stable across runs or prefix-cache collapses | 5 | W1 | — |
| 605 | DeepSeek API | Approval callback that blocks on a channel needs ctx-aware select on BOTH ends | 2 | W1 | — |
| 612 | DeepSeek API | Go's constant float→int conversion isn't auto-applied | 0 | W1 | — |
| 618 | DeepSeek API | Today's date belongs in the system prompt, but only if captured ONCE per session | 9 | W1 | — |
| 625 | DeepSeek API | Provider model-alias sunset dates silently orphan string-literal references | 0 | W1 | — |
| 632 | DeepSeek API | DeepSeek's peak/off-peak window semantics flipped on 2026-08-16 — "off-peak is the default" inverts tier logic | 0 | W1 | — |
| 639 | DeepSeek API | Side-channel Chat calls fail at marshal time on image-bearing history | 3 | W1 | — |
| 646 | DeepSeek API | V4.1 launch: retired model ids keep ANSWERING — client rate cards drift silently | 0 | W1 | — |
| 653 | DeepSeek API | DeepSeek pricing pages are single-currency per language — cross-currency diffs flag phantom rate changes | 0 | W1 | — |
| 660 | DeepSeek API | The tools-family rates have LEFT the pricing pages entirely (2026-11-18 check) | 0 | W1 | — |
| 667 | DeepSeek API | /effort max turns hit finish_reason=length at 16k — seek's explicit cap undercut DeepSeek's implicit thinking budget | 5 | W1 | — |
| 674 | DeepSeek API | TTS 的 64 KiB 请求体上限被 10k 计费字符上限数学遮蔽——写 gate 前先做代数 | 0 | W1 | — |
| 683 | Go language | `filepath.Base` only splits on the host OS's separator — a backslash traversal guard that passes on Windows fails on unix | 0 | W1 | — |
| 690 | Go language | DeepSeek rejects assistant messages with neither `content` nor `tool_calls` | 6 | W1 | — |
| 697 | Go language | DeepSeek HTTP 5xx and empty SSE bodies are transient — retry once before failing | 7 | W1 | — |
| 704 | Go language | Backticks in raw string literals close the string | 1 | W1 | — |
| 710 | Go language | Shadowing the `cap` builtin in a local helper | 0 | W1 | — |
| 716 | Go language | `for j, r := range string` gives BYTE indices, not rune indices | 0 | W1 | — |
| 723 | Go language | Literal UTF-8 BOM in a Go string literal is a compile error | 0 | W1 | — |
| 730 | Go language | Top-level `var` slice and `func` that reference each other → init cycle | 1 | W1 | — |
| 736 | Go language | `json:",omitempty"` is silently ineffective on `time.Time` and other struct types | 3 | W1 | — |
| 743 | Go language | Word-level vs character-level similarity for LLM-produced trait merging | 3 | W1 | — |
| 750 | Go language | A background process must NOT inherit the turn ctx, or it dies when the turn ends | 4 | W1 | — |
| 757 | Go language | go flag: a requested -h is a VALUE (flag.ErrHelp), and ExitOnError hides the verb surface | 1 | W1 | — |
| 768 | Tooling / environment | jq without input files and without -n runs the filter zero times — empty output, exit 0 | 0 | W1 | — |
| 775 | Tooling / environment | Grep tool with broad pattern can deadlock the agent — no recovery via /compact | 11 | EV | 既有：grep-refine-not-flood |
| 782 | Tooling / environment | Path-string assertions / raw paths in JSON literals broke on windows-latest CI | 0 | W1 | — |
| 789 | Tooling / environment | edit tool byte-matches fail on Windows CRLF Go files when old_string uses LF | 0 | W1 | — |
| 796 | Tooling / environment | Go's `time.ParseDuration` rejects "30d" / "1w" — only ns…h | 0 | W2 | stdlib 限制已在 flag 帮助文案文档化 |
| 803 | Tooling / environment | macOS APFS made `SKILL.md` + `skill.md` tests collide silently | 0 | W1 | — |
| 810 | Tooling / environment | Auto-mode classifier blocked inline API keys on the command line | 0 | W3 | 流程纪律：key 走 .env，不改分类器 |
| 816 | Tooling / environment | `go run ./cmd/seek` is slow enough to feel broken | 0 | W3 | 操作习惯：go build -o 替代 go run |
| 822 | Tooling / environment | macOS bash 3.2 has no `mapfile` / `readarray` | 0 | W1 | — |
| 829 | Tooling / environment | `omitempty` on a slice field omits both nil AND empty (`[]T{}`) | 0 | W1 | — |
| 836 | Tooling / environment | `json.Encoder.Encode()` always appends `\n` — the JSONL primitive | 0 | W1 | — |
| 843 | Tooling / environment | `edit` tool: small models burn turns retrying byte-mismatched `old_string` | 8 | EV | 既有：edit-unicode-read-first |
| 850 | Tooling / environment | `/compact` panicked with nil Session on `--no-save` path | 0 | W1 | — |
| 859 | Tooling / environment | Cumulative cost re-priced retroactively when model or tier changed mid-session | 3 | W1 | — |
| 866 | Tooling / environment | Cumulative prompt tokens are meaningless as a context-limit signal | 9 | W1 | — |
| 873 | Tooling / environment | `finish_reason="length"` looks like a normal stop | 4 | W1 | — |
| 879 | Tooling / environment | Windows System32 `sort.exe`/`find.exe` shadow POSIX tools in cygwin/msys shells — "Input file specified two times." | 0 | W1 | — |
| 886 | Tooling / environment | grep's hidden-entry filter ate the walk ROOT when path was "." — every cwd-wide search returned "no matches" | 2 | W1 | — |
| 893 | Tooling / environment | grep crashed the whole process on a pattern ending in `**` (empty-suffix index panic) | 2 | W1 | — |
| 900 | Tooling / environment | `go get -tool pkg@latest` quietly bumps the go directive and runtime deps; analysis CLIs are pinned by the toolchain's export-data version, not just semver | 0 | W1 | — |
| 907 | Tooling / environment | macOS bash 3.2: fullwidth punctuation after `$var` joins the variable NAME, and a successful EXIT-trap command masks the real exit code | 0 | W1 | — |
| 914 | Tooling / environment | modernize -fix turns `s[i].Field = x` into `v.Field = x` under slices.Backward — a silent write-to-copy no-op | 0 | W1 | — |
| 921 | Tooling / environment | 无头浏览器验收脚本三连坑：await 死亡 Promise 当查活、bash 拼 URL 双斜杠 307、跨块 textContent 无分隔 | 0 | W1 | — |
| 928 | Tooling / environment | 新验收脚本忘了登记 selftest.sh → 验证车道拒跑、整卡 unverified 停人审（已机制化：登记制→发现制） | 0 | W1 | — |
| 935 | Tooling / environment | env 的 -u 排在 KEY=VALUE 赋值之后会被当命令名执行（rc=127、静默空捕获） | 0 | W1 | — |
| 946 | LLM provider quirks (M6) | Anthropic requires consecutive tool results merged into ONE user message | 1 | W1 | — |
| 953 | LLM provider quirks (M6) | Gemini omits tool call IDs; system message is `systemInstruction`, not a messages-array entry | 3 | W1 | — |
| 960 | LLM provider quirks (M6) | New overlay panel consumed Ctrl+C — user couldn't quit while help was open | 2 | W1 | — |
| 967 | LLM provider quirks (M6) | OpenAI streaming token counts require `stream_options: {include_usage: true}` | 0 | W1 | — |
| 974 | LLM provider quirks (M6) | GLM (bigmodel.cn paas/v4) is streaming-tool_calls compatible; `reasoning_content` deltas are silently dropped | 2 | EV | 既有：glm-lane-contract |
| 981 | LLM provider quirks (M6) | Zhipu bills by URL, not by key: a Coding Plan key on /api/paas/v4 charges the recharge balance (or errors 1113) | 2 | W1 | — |
| 989 | LLM provider quirks (M6) | propose in print mode always errors "askuser: no callback registered" — plan approval can't be picked headless | 6 | FC | 无头 askFn 未接线仍成立（见 §2 FC-1） |
| 996 | LLM provider quirks (M6) | pricing.Cost bills input ONLY via cache-hit/miss buckets — every second-tier turn priced input at $0 | 3 | W1 | — |
| 1003 | LLM provider quirks (M6) | OpenCode Go: the model list needs no key, the base URL has three routable aliases, and each new lane re-invents the hardcoded-endpoint bug | 12 | W1 | — |
| 1012 | LLM provider quirks (M6) | Vision capability was a one-id allow-list inside pkg/deepseek — every second-tier lane silently lost pasted images | 4 | W1 | — |
| 1018 | LLM provider quirks (M6) | Switching eval off DeepSeek is a request-level claim — the compatible lane eats DEEPSEEK_API_KEY and config beats --base-url | 0 | W1 | — |
| 1029 | Testing / CI | t.Cleanup(chdir) after t.TempDir() breaks Windows TempDir removal | 0 | W1 | — |
| 1036 | Testing / CI | Two `time.Now()` calls in the same nanosecond make Equal-based timestamp assertions flaky | 1 | W1 | — |
| 1043 | Testing / CI | Asserting zero collisions over N random IDs is a birthday-paradox flake trap | 0 | W1 | — |
| 1050 | Testing / CI | SSE test hangs in `srv.Close()` when the response body only closes via `t.Cleanup` | 1 | W1 | — |
| 1057 | Testing / CI | Handler parked on `<-r.Context().Done()` with an unread request body hangs `srv.Close()` forever | 1 | W1 | — |
| 1064 | Testing / CI | `r.FormFile` can't see multipart FIELD ORDER — walk `MultipartReader` parts to assert it | 0 | W1 | — |
| 1071 | Testing / CI | Exec-based E2E binary can't live in any test's `t.TempDir()` — own its lifecycle in `TestMain` | 0 | W1 | — |
| 1080 | Release / upgrade | `tui.VersionString()` is a formatted banner, not a raw module version | 1 | W1 | — |
| 1086 | Release / upgrade | Atomic self-replace requires same-filesystem temp file | 0 | W1 | — |
| 1092 | Release / upgrade | `<br>` inside `<code>` survives display but `textContent` drops it — copy-paste silently corrupts multi-line install commands | 2 | W1 | — |
| 1103 | Site / landing page | Explicit clean-URL rewrites in `_redirects` loop forever on Cloudflare Pages | 0 | W1 | — |
| 1110 | Site / landing page | Pixel "SEEK" banner read as "98%" on mobile — font-glyph block-art is not portable | 0 | W1 | — |
| 1117 | Site / landing page | README hero SVG clipped on the right on GitHub — its monospace is wider than local Menlo | 0 | W1 | — |
| 1137 | Go | Function-local type can't be referenced from another function | 0 | W1 | — |
| 1144 | Go | RWMutex not reentrant — unexported helpers can't lock if caller holds lock | 0 | W1 | — |
| 1151 | Go | Adding a slash command changes `/m` prefix-match test expectations | 0 | W1 | — |
| 1158 | Go | Go slice aliasing: a returned struct with a slice field shares the backing array | 1 | W1 | — |
| 1165 | Go | LLM thinking-mode artifact tokens leaked into generated source code | 4 | FC | post-write 过滤/CI 闸未建（见 §2 FC-2） |
| 1172 | Go | Time-dependent sort key breaks prefix-cache byte-stability | 4 | W1 | — |
| 1179 | Go | System prompt advertised non-existent `limit` parameter → resolved by accepting `limit` with max-50 enforcement | 15 | EV | 既有：read-whole-file / read-tail-fact / read-eof-blind |
| 1186 | Go | Permission modes should be reserved for actual permission changes | 6 | W1 | — |
| 1193 | Go | Paste-folding "any key to expand" UX shock → marker persists, resolved on Enter | 1 | W1 | — |
| 1200 | Go | reviewBranchEntry Esc checked after streaming → Esc cancels stream, not branch entry | 0 | W1 | — |
| 1207 | Go | `s[:n]` on a multi-byte UTF-8 string produces broken runes | 1 | W1 | — |
| 1214 | Go | Custom color tokens (`--dim`) must be verified against WCAG AA in both themes | 0 | W1 | — |
| 1221 | Go | SVG data-URI in CSS `content:` needs explicit width/height on pseudo-element | 0 | W1 | — |
| 1228 | Go | Alt+Enter steer doesn't work on macOS terminals | 0 | W1 | — |
| 1235 | Go | skill_commit must not silently pick a scope — the model has no signal for user intent | 19 | EC | 建议孵化 skill-commit-scope-ask（见 §3.2） |
| 1242 | Go | Slash menu / status bar floated mid-screen on /clear, /help close, and other "banner gone" states | 2 | W1 | — |
| 1249 | Go | Welcome banner pinned across the entire first turn → user input lands above it, screen "auto-clears" at TurnEnd | 6 | W1 | — |
| 1256 | Go | Plan artifact write needs context BEFORE Sink.Approved fires — added a separate ContextReceiver interface | 0 | W1 | — |
| 1263 | Go | `[plan: approved]` is a load-bearing wire-format prefix shared between propose and the plan reconstructor | 10 | W1 | — |
| 1270 | Go | `/help` documented `?` hotkey not implemented; overlay lacked picker pattern | 0 | W1 | — |
| 1277 | Go | Auto-opened picker key-passthrough allow-list is a second, out-of-sync registry | 0 | W1 | — |
| 1284 | Go | Legacy conhost (blue PowerShell 5.x window) TUI streaming cascades (use Windows Terminal) | 0 | W2 | 支持面文档化：WT 为支持宿主，不做 per-emulator 适配 |
| 1291 | Go | WM_SETTINGCHANGE broadcast blocks startup on busy Windows desktops | 0 | W1 | — |
| 1298 | Go | Windows CRLF paste without bracketed mode submits after the first line | 0 | W1 | — |
| 1305 | Go | Child process steals /dev/tty, Esc can't interrupt bash | 3 | W1 | — |
| 1312 | Go | Setsid + pipe stdout = orphan grandchildren deadlock Wait() | 0 | W1 | — |
| 1319 | Go | V4-Flash prediction returns empty content when Thinking isn't explicitly disabled | 5 | W1 | — |
| 1326 | Go | Cross-compile failure: platform-specific syscall in non-tagged file | 0 | W1 | — |
| 1333 | Go | WSL sudo escapes process-group kill, bash Wait() hangs past timeout | 2 | W1 | — |
| 1340 | Go | ObserveEnqueue goroutine captures entry parameter by reference | 2 | W1 | — |
| 1347 | Go | /proc/PID/children empty on CI — descendant walk returns nothing | 0 | W1 | — |
| 1356 | Worktree / Windows | Worktree-isolated subagent edited the MAIN tree (isolation silently bypassed) | 0 | W1n | 修复已落地：permission.Resolve（permission.go:876）实证；词条 left-uncommitted 注记已过时 |
| 1363 | Worktree / Windows | /worktrees panel empty on Windows despite seek-managed worktrees existing | 0 | W1 | — |
| 1370 | Worktree / Windows | never-discarded worktree refs outlive every GC (six June zombies, 2026-11-14) | 0 | W1 | — |
| 1379 | Sandbox / OS jail | Landlock create_ruleset returns EINVAL if you "handle" an access right the kernel's ABI doesn't know | 1 | W1 | — |
| 1388 | Cron parser | `*` wildcard is not a number — must be handled before `strconv.Atoi` in field-item parser | 0 | W1 | — |
| 1397 | Feishu / external API | Feishu IM API returns HTTP 200 on business errors — must parse body `code`, and `content` is double-encoded JSON | 3 | W1 | — |
| 1406 | Subprocess / secrets | Go's `exec.Cmd` with nil `Env` hands the child EVERY parent variable — including the API key | 0 | W1 | — |
| 1415 | Tool output | Head-only truncation of command output drops the verdict and produces a SILENT wrong answer | 6 | W1 | — |
| 1422 | Tool output | git tool: a duplicated subcommand arg surfaces git's cryptic "ambiguous argument" error | 9 | EV | 既有：git-subcommand-shape；词条 uncommitted 注记已被 auto-fix 卡落地 |
| 1431 | Testing | Cache-hit tests against a fake backend prove the parser, not the behaviour | 6 | W1 | — |
| 1438 | Testing | Injecting filesystem failure for cross-OS tests: chmod lies, file-as-dir is honest | 1 | W1 | — |
| 1445 | Testing | `t.Setenv` inside a `t.Parallel` test panics — and the panic surfaces from a helper, not the test | 0 | W1 | 纪律仍生效（复核）：全仓 8 个测试文件、95 处引用 `persistSessionTestFixture` / `withSeekHome`（涉及 81 个 Test 函数），无一同时声明 `t.Parallel()`；`internal/tui/persist_test.go:68` 注释即此规矩 |
| 1455 | Tool decorators | A Tool decorator silently drops the optional interfaces the agent upcasts to | 1 | W1 | — |
| 1462 | Tool decorators | A tool result appended on the success path vanishes whenever the tool also returns an error | 6 | W1 | — |
| 1471 | Filesystem / model safety | `write` had no staleness protection because `edit`'s exact-match made everyone assume both were safe | 0 | EV | 既有：write-guard-fallback |
| 1478 | Filesystem / model safety | Check-then-write is a blind-overwrite guard with a hole in it | 2 | W1 | — |
| 1485 | Filesystem / model safety | A size+mtime freshness token misses the rename-over, which is how most tools change files | 1 | W1 | — |
| 1492 | Filesystem / model safety | Attributing a failure to the sandbox needs three gates, because the errno text alone is worthless | 2 | W1 | — |
| 1499 | Filesystem / model safety | A read that reached EOF but got elided still vouched for the whole file | 4 | EV | 既有：write-elided-edit-fallback |
| 1508 | Tooling / line endings | A/B eval binaries built from the same commit share the -version string, so run.sh writes both sides into ONE results file | 0 | W1 | — |
| 1515 | Tooling / line endings | jq `a // b as $x \| c` binds `as` INTO the `//` alternative — the pipeline disappears | 0 | W1 | — |
| 1522 | Tooling / line endings | core.autocrlf + editing .sh on Windows yields CRLF, which strict bash (WSL) rejects while git-bash silently tolerates | 0 | W1 | — |
| 1529 | Tooling / line endings | Hand-rolled readLine dropped bufio.Scanner's dropCR — CRLF lines gained an invisible \r | 2 | W1 | — |
| 1538 | TUI / session stats | `-resume` silently downgraded every session to the provider-default model | 0 | W1 | — |
| 1545 | TUI / session stats | Measure the LLM-time span at the assistant MessageEnd, not the first ToolExecStart | 5 | W1 | — |
| 1552 | TUI / session stats | Windows clock granularity: `time.Since` returns exactly 0 across sub-tick gaps — timing tests need a sleep | 0 | W1 | — |
| 1559 | TUI / session stats | After /compact the status bar kept showing the summariser's full-history ctx% until the next turn | 2 | W1 | — |
| 1566 | TUI / session stats | Flat search cost is not resumed — Tracker only re-prices TOKENS | 6 | W2 | 接受的取舍：flat cost 不跨 resume，不为 $0.0007/次扩 schema |
| 1575 | CI / gates | A self-skipping test suite makes a missing CI secret look like a pass | 0 | W1 | — |
| 1582 | CI / gates | A benign log line sharing a prefix with a fatal one turns every ordinary failure into a false diagnosis | 0 | W1 | — |
| 1589 | CI / gates | Prose rules do not stop documentation accretion — the files that are read every session need a mechanical ceiling | 1 | W1 | — |
| 1598 | Eval / tool descriptions | Explicit bash bans in prompts underperform tool-description guidance — measured | 22 | EV | 既有：git-multi-query-no-bash（--list 榜首 22 分） |
| 1605 | Eval / tool descriptions | The per-turn `workflowReminder` taught `read→grep→read` while AGENTS.md and the book promised `grep→read` | 2 | W1 | — |
| 1612 | Eval / tool descriptions | `sysprompt.go` rule 5 ("denials → stop") discarded the Hint-carrying denial design | 6 | EV | 既有：print-deny-semantics |
| 1619 | Eval / tool descriptions | Eval prompts that demand verbatim quoting silently break when the target line crosses read's 1024-byte cap | 8 | EV | 既有：tool-selection / bash-overreach |
| 1626 | Eval / tool descriptions | Default-depth `list_dir` answers nothing about a tree — and its output never said so (bash-overreach alternation) | 7 | EV | 既有：bash-overreach（重校后同族） |
| 1634 | Eval / tool descriptions | mcp.json 的键写错层级会被 encoding/json 静默忽略——A2 评估臂可无声等同 A3 | 7 | W1 | — |
| 1642 | Build tags / dependency isolation | A package whose .go files are ALL tag-excluded errors on direct reference, silently tolerated by `./...` | 0 | W1 | — |
| 1651 | ACP / editor & headless lanes | Session cwd has TWO resolution conventions — anchoring only one leaves half the toolbox in the daemon's dir | 0 | W1 | — |
| 1658 | ACP / editor & headless lanes | ACP session/load is an ORDER contract, and the id it loads must outlive the process | 10 | W1 | — |
| 1665 | ACP / editor & headless lanes | Live state captured by closures must be MUTATED in place, not reassigned | 2 | W1 | — |
| 1672 | ACP / editor & headless lanes | Late SetPref desyncs the permission gate from the baked-in sysprompt Mode label | 11 | W1 | — |
| 1679 | ACP / editor & headless lanes | Headless ACP can't choose its provider by flag — and a stray ANTHROPIC_API_KEY in the inherited shell wins the auto-detect | 0 | W1 | — |
| 1686 | ACP / editor & headless lanes | ProjectID hashes the path STRING, not the directory — one physical dir via /tmp and /private/tmp is two projects | 1 | W2 | 决策 c_4c6dar：ProjectID 保持纯函数（canonical-path 契约），测试钉死 |
| 1693 | ACP / editor & headless lanes | ACP configOptions: the dropdown exists only if session/new RETURNS it — and the select value is a bare string with no "type" field | 5 | W1 | — |
| 1699 | ACP / editor & headless lanes | http.Server.Shutdown never returns while SSE handlers are mounted | 0 | W1 | — |
| 1706 | ACP / editor & headless lanes | Subagent continuation must mint a NEW sub_sid — same-id resurrection corrupts the fold | 4 | W1 | — |
| 1713 | ACP / editor & headless lanes | 面向模型的 id 参数必须接受模型实际看得到的形态——agent 脚注只露 sub-sid 短后缀 | 6 | EV | 既有：session-id-contract |
| 1720 | ACP / editor & headless lanes | PRD said "sessions are archived per-project" — they are not; bucket by header CWD | 2 | W1 | — |
| 1727 | ACP / editor & headless lanes | WebFrontend's cfgErr early-return left RPCRegistry() nil — pre-Run wiring panics on gated-out addresses | 3 | W1 | — |
| 1734 | ACP / editor & headless lanes | ACP ToolCallContent has NO shape that validates on every SDK generation — diff blocks do; text blocks silently degrade | 7 | W1 | — |
| 1743 | ACP / editor & headless lanes | Flipping loadSession on exposes every id ever handed out under the old scheme | 4 | W1 | — |
| 1762 | Eval / results data shape | Early eval result files are pretty-printed concatenated JSON, not one-per-line JSONL | 0 | W1 | — |
| 1769 | Eval / results data shape | expect.json 新增标量键撞上 `max_/min_/exact_` 界前缀扫描——每轮全红 "unknown metric" | 1 | W1 | — |
| 1778 | Transcript analysis / offline metrics | read-argument key is schema-dependent; single-key parsing silently buckets the misses into a fake "one incredibly popular file" | 14 | W1 | — |
| 1785 | Transcript analysis / offline metrics | Duplicate-fingerprint rate has two defensible denominators; quoting one as the other shifts the headline 3.6% ↔ 6.2% | 4 | W1 | — |
| 1792 | Transcript analysis / offline metrics | A frozen baseline keeps asserting a shell-parameter shape that has since been changed | 4 | W1 | — |
| 1799 | Transcript analysis / offline metrics | Committed metric snapshots must not embed absolute home paths | 4 | W1 | — |
| 1808 | Typed agents / orchestration | Routing built-in agent types through the custom gate silently widens their toolset | 0 | W1 | — |
| 1815 | Typed agents / orchestration | A delete path built by filepath.Join(user input) escapes scope — Create's Parse gate covers writes only | 0 | FC | skillmgr.Uninstall 名字闸缺失（见 §2 FC-3） |
| 1824 | Git / merge | Concurrent drive merges on the SHARED main checkout silently erase uncommitted edits — do card work in a worktree | 0 | W1 | — |
| 1831 | Git / merge | Merge commits shipped with leftover conflict markers (twice) | 0 | W1 | — |
| 1838 | Git / merge | 纯搬移拆文件时 `git diff -M` 检不出 rename——一份源拆成两份，配对是一对多 | 0 | W1 | 方法论条目（Fix = 等值口径随拆分形态变，`c_3mny65` 已执行）；三个文件锚 `cmd/seek/main.go` / `planbridge.go` / `subagent_glue.go` 均在库 |
| 1847 | Subprocess / streaming stdout | StdoutPipe forbids concurrent Wait — kill-able line-streaming needs cmd.Stdout=io.Writer + io.Pipe | 0 | W1 | — |
| 1854 | Subprocess / streaming stdout | exec'ing an extensionless go build output fails on Windows as "not found" — CreateProcess needs the .exe name | 0 | W1 | — |
| 1863 | Web frontend / asset plane | session/messages elements arrive as objects after r.json() — JSON.parse on them throws and silently drops every message | 0 | W1 | — |
| 1870 | Web frontend / asset plane | macOS bash 3.2 under set -u mangles $VAR followed by a multi-byte character ("TMP\ufeff: unbound variable") | 0 | W1 | — |
| 1877 | Web frontend / asset plane | `${VAR-default}` under set -u passes a set-but-EMPTY value through — the `:-` form is the house discipline; and mktemp'd $tmp must be canonicalised before string-matching it | 1 | W1 | — |
| 1884 | Web frontend / asset plane | W1.2 Host allowlist must match the host PART only — literal bind-addr matching 403s the documented tailnet posture and every httptest hit | 0 | W1 | — |
| 1890 | Web frontend / asset plane | A blanket "POST needs Origin" CSRF gate breaks the script face — the W1.2 write gate is credential-conditional | 0 | W1 | — |
| 1897 | Web frontend / asset plane | A single-file page cannot ship bare CSP `default-src 'self'` — hash-pin the inline blocks instead (and the hash guards only element content) | 0 | W1 | — |
| 1904 | Web frontend / asset plane | A gzip middleware must both PRESERVE http.Flusher and EXCLUDE text/event-stream — either miss kills SSE silently | 0 | W1 | — |
| 1911 | Web frontend / asset plane | http.DefaultClient silently asks for gzip and strips Content-Encoding — the "identity path" test passed vacuously | 0 | W1 | — |
| 1918 | Web frontend / asset plane | `mktemp -t name` is BSD-only — GNU coreutils reads the argument as a template and dies "too few X's" | 0 | W1 | — |
| 1925 | Web frontend / asset plane | Asserting on SSE reply payloads with angle brackets fails — Go's json.Marshal HTML-escapes by default | 0 | W1 | — |
| 1931 | Web frontend / asset plane | Store.Load returns raw un-repaired transcripts — orphan tool_calls reach transcript readers | 2 | W1 | — |
| 1938 | Web frontend / asset plane | WebFrontend.Send is not a pure broadcast — it pops the conv's live run (the W1.4 close-out) | 0 | W1 | — |
| 1945 | Web frontend / asset plane | getUserMedia needs a secure context — the documented tailnet `--tcp` forward cannot record push-to-talk | 0 | W1 | — |
| 1951 | Web frontend / asset plane | strings.Contains on a RAW JSON frame can never match a Windows path or angle bracket — needles need the escaped spelling too | 0 | W1 | — |
| 1957 | Web frontend / asset plane | go:embed serves the WORKING-TREE bytes — a Windows autocrlf checkout drifts a hash-pinned embedded asset | 0 | W1 | — |
| 1963 | Web frontend / asset plane | web-gate 的 size 基线钉在「哪个工具链量的」上——±1.8MB 的 skew，棘轮要按 delta 加不要按绝对值重测 | 0 | W1 | — |
| 1970 | Web frontend / asset plane | 页面新增一个静态资产，两个既有无头验收的 scripts=1 基线同刻全断——资产数变化要顺手改全仓 script-count 钉 | 0 | W1 | — |
| 1979 | Tooling / go list | go list stops flag-parsing at the first package argument — a trailing `-f` becomes a package named "-f" | 1 | W1 | — |
| 1986 | Tooling / go list | `git show --numstat` emits `-\t-` for binaries and one `old => new` line for renames | 2 | W1 | — |
| 1995 | Skill stats / --idle | `--idle` must read last_used from the UNfiltered aggregate — the window-filtered one reports every idle skill as "never" | 0 | W1 | — |
| 2004 | MCP bridge / tool naming | 同一份 mcp.json 两次启动可能注册出不同的工具名 | 0 | W1 | — |
| 2013 | Setup wizard / provider menu | providerOptions 扩行需三处同步——setup.go 的 envVarFor 镜像漏更会静默误触导向导 | 0 | W1 | — |
| 2022 | MCP prompt section | JSON object key order is data — unmarshalling into a map shuffles prompt bytes | 10 | W1 | — |
| 2031 | DeepSeek Files API / file_id | file_id 引用参与前缀缓存——文档按「解析后的文字」进 prompt，id 本身不是 token | 2 | W2 | 读数即交付：file_id 参与前缀缓存的实测结论，无代码改动 |
| 2038 | DeepSeek Files API / file_id | POST /files 响应里没有 page_count / expires_at——按页计费与过期重传都拿不到数据 | 0 | FC | 按页计费恒 $0（见 §2 FC-4） |
| 2045 | DeepSeek Files API / file_id | 同一份 PDF 的两次上传，解析结果并不总是完整——上传返回 id ≠ 全部页面已进上下文 | 2 | W2 | 观察记录：服务端解析完整性未定论，烟测只断言第 1 页 |
| 2053 | DeepSeek Files API / file_id | Adding a required field to a tool schema: sibling interface for the wiring, a declared cache bust for the cost | 10 | W1 | — |
| 2062 | Lane guards / refusal wording | 闸的拒绝文案是给模型的指令面——写错场合等于教模型绕过 | 0 | EC | 建议孵化 deny-hint 处置回归（见 §3.2） |
| 2070 | Distill review `s` / memory↔skill bridge | 模态内异步结果消息的 stale 判定不能只带位置参数——idx 回绕后旧消息会拼接进新回合 | 0 | W1 | — |
| 2077 | Distill review `s` / memory↔skill bridge | trigram-Jaccard 阈值 fixture 手算两错同一处——先 sanity assert 再断行为 | 0 | W1 | — |
| 2084 | Distill review `s` / memory↔skill bridge | converting 吞 `n`，且「状态位当世代号」在允许退场重发后失效——风险表缓解条款即契约 | 1 | W1 | — |
<!-- triage-table:end -->

## 6. 再生成口径（幂等验证法，验收③）

- 三个输入：①当前 `docs/pitfalls.md`（解析同 `scripts/pitfall-to-eval.sh` 的 `parse_entries`：`---` 之后、跳过 `Short symptom title` 模板、`### ` 起始行 = 行号锚）；②同一套 behavioural 打分（标题 + Fix + Lesson 的加权和，复制自该脚本）；③**本文件上一版的 §5 表**（按完整标题继承去向与注记）。
- 输出：整体覆盖 §5 表区间；散文节（§0–§4、§7）人工维护，不在生成器里。
- **幂等实测（本轮真跑过）**：把本文件的 §5 表读回 → 以「当前台账 + 读回的去向/注记」重算整表 → 与本文件内嵌表**逐行逐字节比较**：`270 行 / 逐字节相同（byte-identical: True）`。同一份 `pitfalls.md` 重跑永远得到同一张表：表按行号升序、无追加路径、无时间戳、无计数器。
- **可复核命令**（不新增仓内脚本，这里给一次性口径）：解析 = `awk`（行为同 `scripts/pitfall-to-eval.sh` 的 `parse_entries`：`---` 之后、跳过 `Short symptom title`、`### ` 起始行）；打分 = 同一段 awk 的 behavioural 加权；继承 = 按**完整标题**（去掉 `\|` 转义后精确匹配）。三段拼起来跑一遍，与 §5 表 `diff` 空即通过。
- **锚点会漂、注记不会重**：台账新增词条使后续行号整体下移（本轮实测的偏移是 `+0 / +7 / +15 / +22` 四段，见 §7.1）；漂移只影响行号列，去向与注记按标题继承，所以**重复执行不会产生第二条注记**。新词条进台账时继承会失败并打印未分流标题清单——那是「下一轮 triage 全量重生成」的预期入口，不是静默默认值（本轮就撞到 3 条，§7.1 C）。
- 后续动作分工：FC 五张由后续开卡直接引用 §2 的行号 + 证据；absorbed 硬候补 0、边缘 2 条（`:575` / `:582`）由下次 archive pass 按台账 :27-33 人判；EC 两条由 eval 维护者 `scripts/pitfall-to-eval.sh --make 1235` / `--make 2062` 孵化（README / prompt 草稿即起，`expect.json` 仍是人审闸）。

## 7. 本轮自查记录（对照分支现状，逐条如实写回）

### 7.1 已修正的偏差

| # | 偏差 | 事实 | 本轮处置 |
|---|---|---|---|
| A | 首版锚在旧 blob 行号（`d9dd365`，267 条），但分支已 3 次 merge main | 270 条 live；267 条旧行里 **111 条的旧行号在今天的文件里不是 `###` 起始行**，**210 条行号已偏移**（仅 57 条未动） | §5 全部**改锚当前行号**（逐条按标题对齐后重算），并根据新行号更新 §2/§3/§4 的引用 |
| B | 首版 §2/§3/§4 的行号引用（`:982` `:1158` `:1800` `:2016` `:548` `:1228` `:2040` `:568` `:575`） | 全部随 3 条新词条下移，引用会指错词条 | 逐条重算：`:989` `:1165` `:1815` `:2038` `:555` `:1235` `:2062` `:575` `:582` |
| C | 首版 §0 说「267 live」 | 现为 270（合并进来的 3 条：`/compact` 救援、`t.Setenv`+`t.Parallel`、`git diff -M` 拆文件 rename） | §0 改为 270，三条新词条在 §5 补齐去向（均判 W1，证据见注记） |
| D | 首版 §5 的**节列**在 41 处标错 | 每个节**最后一条**词条被标成了**下一个节**的名字（例：`:104` Topic fanout 属 Filesystem / concurrency，首版写 Hook / memory）；原因是生成器在节边界上取了后一个 `## ` | 本轮按「向上最近 `## `」重算节列，41 处全部纠正 |
| E | 首版行内计量表述「留观 247」 | 267 − 5 − 2 − 13 = 247 ✓（旧值本身对） | 新基数下为 **250**：270 − 5 − 2 − 13 |

### 7.2 复核后仍成立、无需改动的判断

- FC-1..FC-5 五条**证据全部复现**（见 §2 各行）；EC-1/EC-2 两条**仍未孵化**（`eval/cases/` 无同名 case）；EV 13 条的 case 目录**全部存在**；W1n 5 条、W2 9 条、W3 2 条的理由本轮逐条复读，仍成立。
- 首版的三条落盘选型理由（独立文件 / 不动词条 / archive pass 职责）本轮复核仍成立，故不改选型。

### 7.3 发现但不属于本卡范围（留作后续编辑/回收，本 triage 不动词条）

| # | 词条 | 问题 | 归谁 |
|---|---|---|---|
| F | `:796` | Refs 指向 `cmd/seek/skill_query.go:cmdSkillStats`——该文件已不存在；`cmdSkillStats` 现在 `internal/skillcli/query.go:428`（`--since` 的帮助文案「24h, 168h, 720h」仍原样在 :438） | 下一把「台账编辑刀」（改词条正文属于 ledger 编辑，不属 triage） |
| G | `:1734` | Refs 里的 `2619232` / `2622104` 看着像 commit 短哈希，实为 **paseo.app asar 内的字节偏移**（原句已写 `~byte`）；本仓无此 commit，不是失效锚，只是形态容易被误读 | 无需处理；若日后编辑该条，建议把 `byte` 前缀写得更显眼 |
| H | `:928` / `:1582` / `:1970` | Refs 里的 `docs/mechanisms.md`（butler 仓）、`docs/postmortem/0004-…`（dsh 仓）、`scripts/img/`（页面元素计数口径的简写，非路径）——**跨仓 / 简写，不是本仓失效锚** | 无需处理，记录以免下次复查重复排查 |
| I | `:2053`、`:1589`、`:1948` | 曾疑为失效锚，实测**全部有效**：`docs/release-notes-v0.12.1.tagmsg` 在库；`scripts/doc-budgets.tsv` + `scripts/verify-doc-budgets.sh` 都在（词条简写为 `scripts/doc-budgets`）；`scripts/web-*-headless.mjs` 三件都在 | 无需处理（记录为「已排查、非缺陷」，省下一轮重复劳动） |
| J | 卡面「候选 8 条坑的下游」 | 本分支读数：FC 5 + EC 2 = 7 条可开动作；若把 2 条边缘 absorbed 候补也算上则 9 条 | 未凑数；若上游确指某 8 条，需要卡面/上游文档给出名单再对账 |
| K | （新坑，未写入台账） | 本轮自造的生成器第一版把**标题按 56 字符截断**后再当 join key 用，读回来无法还原完整标题 → 去向继承直接 KeyError。教训：「用显示用的截断文本当 join key」是自伤；纯展示才截断，键必须留全 | **故意不写进 `docs/pitfalls.md`**：此刻新增任何词条都会让本快照的行号与计数（270）当场失效。留给主会话决定——若要入账，须与本轮「重生成 §5」同一动作做（顺序：先入账 → 再重跑 triage） |

### 7.4 验收对照

| 验收 | 状态 | 证据 |
|---|---|---|
| ① 255（现 270）条每条有去向注记 | ✅ | §5 表 270 行，行号 = 当前 `###` 行号；`awk` 复核：行数 270、行号与标题一一对上、无重复行号 |
| ② 统计三数（live / absorbed / 候选卡数）与逐条可核 | ✅ | §0：270 / 0（+2 边缘）/ 5；§5 表尾统计 `FC 5 · EC 2 · EV 13 · W1 234 · W1n 5 · W2 9 · W3 2` |
| ③ 重跑幂等（不重复注记） | ✅ | §6 口径 + 本轮实测（读回表→重跑→`diff` 空）；去向右标签继承，行号漂移不改注记 |
| ④ 机制预算：triage 一次性动作、非常驻 | ✅ | §1：无新脚本 / 不进 CI / 无定时任务 / 无预算条目 |
| ⑤ 架构页同步义务 | ✅ 不适用 | §1：纯台账治理 + 文档写回，不触部署形态与常驻设施 |
