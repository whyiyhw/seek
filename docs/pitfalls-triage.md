# pitfalls 台账分流 triage（2026-11-19，卡 c_yhpfsp）

> 一次性快照，非常驻机制。来源 [`docs/pitfalls.md`](pitfalls.md) @ blob `d9dd365b3dce53ab1a148c83cc351711739341bd`（HEAD `38149dc`），全集 **267 live / 0 absorbed**（`grep -c '^### '` = 268 含 1 条模板；`scripts/pitfall-to-eval.sh --list` 同数 267）。

## 0. 结论速览（验收三数）

- **live：267**（机械口径见上；卡面所引 255 是台账分布表过期 Total，本卡已修正，见 §1.1）
- **absorbed：硬候补 0**（与 2026-11-19 archive pass 结论一致）；**边缘候补 2**（:568/:575，留下次 pass 人判，§4）
- **修复卡候选：5**（§2）
- 其余分流：eval 候选（未孵化）**2** · eval 已覆盖 **13** · 留观 **247**

## 1. 数据面与落盘选型（选型写明）

排序产出：`scripts/pitfall-to-eval.sh --list` → 267 条，score = 模型行为可能性启发式（标题/Fix/Lesson 中的 behavioural 信号加权和）。本 triage 逐条四去向：

| 去向 | 含义 | 判据 |
|---|---|---|
| FC 修复卡候选 | 坑描述的修复至今未做/被显式推迟 | 逐条对源码验证「Fix 是否真的落地」，未落地者列 §2 |
| EC eval 候选 | 模型行为坑，且 eval/cases/ 无既有 case | --list 高分 + 无同名/同主题 case，走既有管道 `--make` 孵化 |
| EV eval 已覆盖 | 模型行为坑，case 已存在 | §3.1 映射表（不再重复孵化） |
| W1/W1n/W2/W3 留观 | 已修在档 / 设计取舍·观察记录 / 环境·纪律 | 理由逐条见表；W1 = 修复已落地（词条 Fix 带 commit/测试锚点）、教训仍立——台账 :31 的「live 正常形态」 |

**为什么落独立文件 `docs/pitfalls-triage.md` 而非台账内注记**：

1. `pitfalls.md` 是每会话被 agent 读的活文档（append-only 记新坑），逐条插入分流注记使其体量近翻倍，且与既有「Status: live vs absorbed」节职责重叠；
2. 本仓纪律（AGENTS.md「Reconstruct state from the transcript」）：**一次性分析快照不是常驻状态**，不镜像进活文档——快照落独立文件、标注源 blob，下轮 triage/archive pass 整体重生成；
3. absorbed 的正式标记（`### [已吸收]` + 尾注）与搬移是 **archive pass 的职责**（规则在台账 :27-33）；本 triage 只提候选、不动任何词条正文。

**幂等（验收③）**：本文件为整体覆盖写生成物。条目锚 =「词条起始行号 + 标题」，重跑生成器 = 全文件替换，不追加、不产生重复注记；`pitfalls.md` 本体只接受了一次数值纠错（§1.1），triage 永不改写词条。

**机制预算（验收④）**：triage 是**一次性动作，非常驻机制**——不新增 `scripts/` 脚本、不进 CI/selftest、无定时任务；生成器为一次性 awk（解析逻辑复制自 `pitfall-to-eval.sh` 的 `parse_entries`，例外表内嵌），用后即弃，口径记录于 §5。**架构页同步义务（验收⑤）：不适用**——本卡为台账治理 + 文档变更，不触 butler 常驻设施、部署形态或任何运行机制，无需同步。

### 1.1 台账分布表纠错（同卡顺带，只改数值不动行）

- `:82` Total **255 → 267**；LLM provider quirks (M6) 10→**11**、Tooling / environment 24→**25**、Web frontend / asset plane 15→**17**；`:84` rollup API 35→**36**、tooling & engineering 143→**154**（34+36+43+154=267）。
- 原表 42 行实际合计 263（Total 手写 255，本就差 8），再叠 4 条新词条 → 差 12。修正后逐行合计 = Total = 机械计数 = `--list` 条数，三者互证。
- 词条行号零移动（只改数值列），本表 §5 的行号锚不受影响。

## 2. 修复卡候选清单（5 张，供后续开卡引用；本卡不开卡）

| # | 行 | 主题（建议卡名起点） | 现状证据（本卡实测） |
|---|---|---|---|
| FC-1 | :982 | 无头车道 askFn 接线（print 模式 propose/ask_user 恒 `ErrNoCallback`） | 仍成立：`cmd/seek/main.go:2404` `runPrint` return 先于 `SetAskFn` 接线（:2421/:2445，均在 TUI 块内）。词条自记「not fixed here (out of card scope)」。开卡方向：auto-deny+打印契约 或 stdin 回调 |
| FC-2 | :1158 | thinking 工件 token 的 post-write 过滤 / CI 闸 | 仍成立：全仓 grep `end▁of▁thinking` 零命中（过滤、CI 闸均未建）。词条 Fix 的「audit the writing tool to post-strip… Alternatively CI」两句均未执行 |
| FC-3 | :1800 | `skillmgr.Uninstall` 名字闸（同族暴露） | 仍成立：`internal/skillmgr/skillmgr.go` Uninstall 仍 `filepath.Join(userDir, opts.Name)` 直拼后 Stat/Remove，无 `ValidName`+`filepath.Rel` 闸（agentsmgr.Remove 同卡已修，词条明记 sibling NOT fixed） |
| FC-4 | :2016 | Files API 按页计费恒 $0 | 仍成立：上传响应无 `page_count`，`internal/fileroute/fileroute.go:158` `ParseCost(fo.PageCount)` = `ParseCost(0)`。词条留给「F5 价格核对卡」定夺：本地数页（仅 PDF 可行）或等 API 补字段 |
| FC-5 | :548 | FIM 比率指标设计（v1.1 遗留） | 低优先：验收判据已降为绝对次数，比率设计（候选：FIM-saved tokens / total prompt tokens）显式 deferred v1.1，等真实用量数据 |

## 3. eval 去向

### 3.1 已覆盖（13 条，case 已存在——不重复孵化）

| 行 | 主题 | 既有 case |
|---|---|---|
| :390 | 拒绝是纯税→自动修 | `git-subcommand-midtask` / `git-subcommand-shape` |
| :768 | grep 大 pattern 塞爆上下文 | `grep-refine-not-flood` |
| :836 | 小模型 byte-mismatch 烧轮次 | `edit-unicode-read-first` |
| :967 | GLM 车道契约族 | `glm-lane-contract` |
| :1172 | read limit 参数面 | `read-whole-file` / `read-tail-fact` / `read-eof-blind` |
| :1415 | git 重复子命令 | `git-subcommand-shape`（词条内 uncommitted 注记已被 auto-fix 卡落地，`TestExecute_DuplicateSubcommandArgAutoFixed` 在库） |
| :1456 | write 陈旧覆盖 | `write-guard-fallback` |
| :1484 | elided read 不担保全文件 | `write-elided-edit-fallback` |
| :1583 | 提示层禁令 vs 工具描述（--list 榜首 22 分） | `git-multi-query-no-bash`（bash_chains / bash_git_calls 指标） |
| :1597 | 拒绝→自恢复（Hint 语义） | `print-deny-semantics` |
| :1604 | eval 提词耦合工具上限 | `tool-selection` / `bash-overreach` |
| :1611 | list_dir 默认深度边界 | `bash-overreach`（重校后同族） |
| :1698 | 模态可见 id 形态 | `session-id-contract` |

（--list 高分条目 22/15/13/11 分位均已有 case 对应——eval 管道对高分坑的覆盖是实的。）

### 3.2 候选（2 条，未孵化——走既有 `scripts/pitfall-to-eval.sh --make <line>`）

| # | 行 | score | case 草图 |
|---|---|---|---|
| EC-1 | :1228 | 19（榜眼） | `skill-commit-scope-ask`：给「把这个 skill 装上」类任务，量测模型是否先以 ask_user 问 scope（user/project）再调 skill_commit，而非默默选边 |
| EC-2 | :2040 | — | 拒绝文案=指令面：收到带处置指引的 deny 后，模型照 Hint 换形/停手而非绕字符串匹配（v0.12.1 推送绕闸事故的模型侧回归）。构造需注意车道：闸在 autopilot/goal 车道，eval 需 reachable 的等价 deny 面 |

## 4. absorbed 候补

- **硬候补 0**：按台账标记规则（「跟随今天会走偏」才算）全量复查，未发现主题被取代且仍误导的词条——与 2026-11-19 archive pass「0 absorbed」独立得出同一结论，互证。
- **边缘 2 条**（W1n 注记在表内，处置权归下次 archive pass 人判）：
  - `:568` deepseek-reasoner 别名静默降级——别名 2026-07-24 已死、常量已清扫（V4.1 世界只剩 `deepseek-flash`），但 `ShouldEnableThinking` 建议仍正确不误导；
  - `:575` legacy 别名日落——「切换默认」动作已发生完毕，词条属已执行历史（清扫记录在同族 :619/:639）。
  - 若判吸收：按规则搬 `pitfalls-archive.md` 时保留 lesson 指向（「同一日落类事故再发时怎么办」的方法论活在 :618/:639）。

## 5. 全量逐条表（267 行，验收①②）

W1 默认注记 = 「修复已落地（词条 Fix 带 commit/测试锚点），教训仍立——live 正常形态」；下表 W1 行不逐条重复此句。

| 行 | 节 | 标题 | 分 | 去向 | 注 |
|---|---|---|---:|---|---|
| 90 | Filesystem / concurre… | Two parallel writers of the SAME content-addressed blob… | 1 | W1 | — |
| 97 | Filesystem / concurre… | SSE unsubscribe defer deleted the WRONG subscriber — `f… | 1 | W1 | — |
| 104 | Hook / memory | Topic fanout keyed coarser than the producer unit — cro… | 0 | W1n | 修复已进 main：webfrontend_topic.go topicFollow 实证（c_3736yl rework 已并） |
| 113 | Hook / memory | OnSessionStart must reset snapshot state for --resume c… | 5 | W1 | — |
| 120 | Hook / memory | ObserveEnqueue accept counter races with non-blocking R… | 0 | W1 | — |
| 127 | Hook / memory | Shell-hook SkipReason needs per-event handling, not a s… | 1 | W1 | — |
| 134 | Hook / memory | Trust prompt must run pre-bubbletea — askuser channel i… | 2 | W1 | — |
| 141 | Hook / memory | limitedWriter needs a pointer receiver — value receiver… | 1 | W1 | — |
| 148 | Hook / memory | A hook registered on the shared Registry fires in EVERY… | 4 | W1 | — |
| 155 | Skill distill (auto p… | dropMalformed must list every Config event slice — Vali… | 2 | W1 | — |
| 164 | TUI / terminal | Same session re-entering the distill window double-coun… | 0 | W1 | — |
| 173 | TUI / terminal | A light palette derived by darkening a dark palette sto… | 0 | W1 | — |
| 180 | TUI / terminal | Status-bar glyph-width oracle mismatch soft-wraps the f… | 1 | W1 | — |
| 187 | TUI / terminal | Explicit `Accept-Encoding: gzip` header disables Go's a… | 2 | W1 | — |
| 194 | TUI / terminal | "starting seek …" placeholder stuck for seconds (or for… | 0 | W1 | — |
| 201 | TUI / terminal | Mouse drag-to-select did nothing | 0 | W1 | — |
| 208 | TUI / terminal | Long streaming replies freeze the screen — inline rende… | 1 | W1 | — |
| 215 | TUI / terminal | Garbage `]11;rgb:fae0/fae0/fae0\[1;1R` in the input box | 0 | W1 | — |
| 222 | TUI / terminal | lipgloss v2 background probe hangs forever when stdin i… | 0 | W1 | — |
| 229 | TUI / terminal | Alt-screen mode breaks scrollback, copy, and content pe… | 1 | W1 | — |
| 236 | TUI / terminal | Inline-mode drift was self-inflicted by floor-pin paddi… | 2 | W1 | — |
| 245 | TUI / terminal | Reserved popup zone was the same mistake as floor-pin p… | 0 | W1 | — |
| 252 | TUI / terminal | Esc-cancelled stream silently dropped the user's prompt | 3 | W1 | — |
| 259 | TUI / terminal | `/model` updated the display but not the Agent's model … | 3 | W1 | — |
| 266 | TUI / terminal | Esc-cancel prompt restore can leak a prior chat prompt … | 3 | W1 | — |
| 273 | TUI / terminal | Agent.Messages() races with the Prompt goroutine when c… | 3 | W1 | — |
| 280 | TUI / terminal | /clear (cmdNew) swaps session/tracker before checking R… | 1 | W1 | — |
| 287 | TUI / terminal | replay.go skipped RoleTool messages and raw-rendered Ma… | 0 | W1 | — |
| 294 | TUI / terminal | `initialSizeCmd` clobbers `teatest.WithInitialTermSize`… | 3 | W1 | — |
| 301 | TUI / terminal | `tm.Output()` is a single-use reader — sequential `Wait… | 3 | W1 | — |
| 308 | TUI / terminal | Ctrl+J newline inserts via `InsertString("\n")` broke c… | 0 | W1 | — |
| 315 | TUI / terminal | Adding a builtin skill is prefix-cache-safe ONLY becaus… | 6 | W1 | — |
| 322 | TUI / terminal | Batch ask_user picker: only the first option could be s… | 0 | W1 | — |
| 329 | TUI / terminal | Windows Terminal forwards the IME-commit Enter — the 50… | 0 | W1 | — |
| 336 | TUI / terminal | Shift+Enter is indistinguishable from Enter — bubbletea… | 3 | W1n | v2 迁移已解决（词条内嵌 RESOLVED 注记）；lesson「框架大版本后重验平台事实」仍通用 |
| 344 | TUI / terminal | Bubble Tea v2 migration: KeyMsg became an interface, pa… | 0 | W1 | — |
| 351 | TUI / terminal | Ctrl+V image paste on Windows was a silent no-op — a mi… | 2 | W1 | — |
| 358 | TUI / terminal | PowerShell `-Command`: everything after it is command T… | 1 | W1 | — |
| 365 | TUI / terminal | Windows terminals swallow Ctrl+V — the key binding was … | 0 | W1 | — |
| 372 | Agent loop | Bare `m.appendHistory(...)` as a statement drops the li… | 4 | W1 | — |
| 383 | Agent loop | Static prompt prose can name tools the request's tools … | 6 | W1 | — |
| 390 | Agent loop | A refusal the model does not learn from is a pure tax —… | 13 | EV | 既有：git-subcommand-midtask / git-subcommand-shape |
| 397 | Agent loop | Esc mid-stream poisoned the session: orphan `tool_calls… | 5 | W1 | — |
| 404 | Agent loop | Followup: ctx-cancel was one of FOUR paths to the same … | 4 | W1 | — |
| 411 | Agent loop | Symlinks inside CWD let `write`/`edit` escape the CWD g… | 0 | W2 | 文档化限制：单用户本地工具威胁模型接受，测试钉死现状 |
| 418 | Agent loop | Symlinked CWD made every RELATIVE `write` compare "outs… | 4 | W1 | — |
| 425 | Agent loop | `Policy.mode` was raced by /yolo flips against concurre… | 0 | W1 | — |
| 432 | Agent loop | `tools.ReadOnlyTool` is the on-switch for parallel disp… | 3 | W1 | — |
| 439 | Agent loop | `seek -upgrade` returned raw "permission denied" — user… | 0 | W1 | — |
| 446 | Agent loop | Tick's "len(due) == 0 → early return" silently skipped … | 2 | W1 | — |
| 453 | Agent loop | OS scheduler invokes `seek cron tick` with a near-empty… | 0 | W1 | — |
| 460 | Agent loop | `cache.Tracker.AdoptChild` had no resume-double-count g… | 1 | W1 | — |
| 467 | Agent loop | `json.Unmarshal` silently drops unknown fields — LLM ty… | 6 | W1 | — |
| 474 | Agent loop | A clean empty completion aborted the turn — but a retry… | 7 | W1 | — |
| 481 | Agent loop | Startup validation of custom agent types must run AFTER… | 0 | W1 | — |
| 488 | Agent loop | agenttypes.Definition zero-Origin reads as a built-in —… | 0 | W1 | — |
| 495 | DeepSeek API | `finish_reason:"refusal"` with empty content is not an … | 2 | W1 | — |
| 504 | DeepSeek API | Server web tools (`deepseek_read_url`/`deepseek_web_sea… | 3 | W2 | 判红关闭的决策记录（PRD §2.2），无修复意图 |
| 511 | DeepSeek API | `reasoning_content` rule for V4 thinking is the OPPOSIT… | 5 | W1 | — |
| 521 | DeepSeek API | Prefix cache hits are best-effort and short prompts don… | 3 | W2 | best-effort 缓存行为已文档化，无代码可修 |
| 527 | DeepSeek API | Empty tool result + `omitempty` on `Message.Content` → … | 3 | W1 | — |
| 534 | DeepSeek API | `reasoning_effort` values are `high\|max` on DeepSeek V… | 0 | W1 | — |
| 541 | DeepSeek API | FIM endpoint is at `/beta/completions` with legacy Open… | 4 | W1 | — |
| 548 | DeepSeek API | FIM usage ratio is inherently low — model prefers edit/… | 6 | FC | FIM 比率指标设计 deferred v1.1（见 §2 FC-5） |
| 555 | DeepSeek API | Streamed tool calls arrive as deltas keyed by `index` | 0 | W1 | — |
| 561 | DeepSeek API | "Respond with JSON only" instructions to the reasoner a… | 4 | W1 | — |
| 568 | DeepSeek API | `deepseek-reasoner` without `Thinking` parameter silent… | 2 | W1n | 边缘 absorbed 候补（§4）：别名已死常量已扫，但建议仍不误导——下次 pass 人判 |
| 575 | DeepSeek API | DeepSeek legacy aliases `deepseek-chat` / `deepseek-rea… | 2 | W1n | 边缘 absorbed 候补（§4）：日落已执行完毕，词条属已执行历史——下次 pass 人判 |
| 582 | DeepSeek API | `<project>/.seek/project-id` is per-machine state — mus… | 0 | W1 | — |
| 589 | DeepSeek API | PrePromptHook output must be byte-stable across runs or… | 5 | W1 | — |
| 598 | DeepSeek API | Approval callback that blocks on a channel needs ctx-aw… | 2 | W1 | — |
| 605 | DeepSeek API | Go's constant float→int conversion isn't auto-applied | 0 | W1 | — |
| 611 | DeepSeek API | Today's date belongs in the system prompt, but only if … | 9 | W1 | — |
| 618 | DeepSeek API | Provider model-alias sunset dates silently orphan strin… | 0 | W1 | — |
| 625 | DeepSeek API | DeepSeek's peak/off-peak window semantics flipped on 20… | 0 | W1 | — |
| 632 | DeepSeek API | Side-channel Chat calls fail at marshal time on image-b… | 3 | W1 | — |
| 639 | DeepSeek API | V4.1 launch: retired model ids keep ANSWERING — client … | 0 | W1 | — |
| 646 | DeepSeek API | DeepSeek pricing pages are single-currency per language… | 0 | W1 | — |
| 653 | DeepSeek API | The tools-family rates have LEFT the pricing pages enti… | 0 | W1 | — |
| 660 | DeepSeek API | /effort max turns hit finish_reason=length at 16k — see… | 5 | W1 | — |
| 667 | Go language | TTS 的 64 KiB 请求体上限被 10k 计费字符上限数学遮蔽——写 gate 前先做代数 | 0 | W1 | — |
| 676 | Go language | `filepath.Base` only splits on the host OS's separator … | 0 | W1 | — |
| 683 | Go language | DeepSeek rejects assistant messages with neither `conte… | 6 | W1 | — |
| 690 | Go language | DeepSeek HTTP 5xx and empty SSE bodies are transient — … | 7 | W1 | — |
| 697 | Go language | Backticks in raw string literals close the string | 1 | W1 | — |
| 703 | Go language | Shadowing the `cap` builtin in a local helper | 0 | W1 | — |
| 709 | Go language | `for j, r := range string` gives BYTE indices, not rune… | 0 | W1 | — |
| 716 | Go language | Literal UTF-8 BOM in a Go string literal is a compile e… | 0 | W1 | — |
| 723 | Go language | Top-level `var` slice and `func` that reference each ot… | 1 | W1 | — |
| 729 | Go language | `json:",omitempty"` is silently ineffective on `time.Ti… | 3 | W1 | — |
| 736 | Go language | Word-level vs character-level similarity for LLM-produc… | 3 | W1 | — |
| 743 | Go language | A background process must NOT inherit the turn ctx, or … | 4 | W1 | — |
| 750 | Tooling / environment | go flag: a requested -h is a VALUE (flag.ErrHelp), and … | 1 | W1 | — |
| 761 | Tooling / environment | jq without input files and without -n runs the filter z… | 0 | W1 | — |
| 768 | Tooling / environment | Grep tool with broad pattern can deadlock the agent — n… | 11 | EV | 既有：grep-refine-not-flood |
| 775 | Tooling / environment | Path-string assertions / raw paths in JSON literals bro… | 0 | W1 | — |
| 782 | Tooling / environment | edit tool byte-matches fail on Windows CRLF Go files wh… | 0 | W1 | — |
| 789 | Tooling / environment | Go's `time.ParseDuration` rejects "30d" / "1w" — only n… | 0 | W2 | stdlib 限制已在 flag 帮助文案文档化 |
| 796 | Tooling / environment | macOS APFS made `SKILL.md` + `skill.md` tests collide s… | 0 | W1 | — |
| 803 | Tooling / environment | Auto-mode classifier blocked inline API keys on the com… | 0 | W3 | 流程纪律：key 走 .env，不改分类器 |
| 809 | Tooling / environment | `go run ./cmd/seek` is slow enough to feel broken | 0 | W3 | 操作习惯：go build -o 替代 go run |
| 815 | Tooling / environment | macOS bash 3.2 has no `mapfile` / `readarray` | 0 | W1 | — |
| 822 | Tooling / environment | `omitempty` on a slice field omits both nil AND empty (… | 0 | W1 | — |
| 829 | Tooling / environment | `json.Encoder.Encode()` always appends `\n` — the JSONL… | 0 | W1 | — |
| 836 | Tooling / environment | `edit` tool: small models burn turns retrying byte-mism… | 8 | EV | 既有：edit-unicode-read-first |
| 843 | Tooling / environment | `/compact` panicked with nil Session on `--no-save` path | 0 | W1 | — |
| 852 | Tooling / environment | Cumulative cost re-priced retroactively when model or t… | 3 | W1 | — |
| 859 | Tooling / environment | Cumulative prompt tokens are meaningless as a context-l… | 9 | W1 | — |
| 866 | Tooling / environment | `finish_reason="length"` looks like a normal stop | 4 | W1 | — |
| 872 | Tooling / environment | Windows System32 `sort.exe`/`find.exe` shadow POSIX too… | 0 | W1 | — |
| 879 | Tooling / environment | grep's hidden-entry filter ate the walk ROOT when path … | 2 | W1 | — |
| 886 | Tooling / environment | grep crashed the whole process on a pattern ending in `… | 2 | W1 | — |
| 893 | Tooling / environment | `go get -tool pkg@latest` quietly bumps the go directiv… | 0 | W1 | — |
| 900 | Tooling / environment | macOS bash 3.2: fullwidth punctuation after `$var` join… | 0 | W1 | — |
| 907 | Tooling / environment | modernize -fix turns `s[i].Field = x` into `v.Field = x… | 0 | W1 | — |
| 914 | Tooling / environment | 无头浏览器验收脚本三连坑：await 死亡 Promise 当查活、bash 拼 URL 双斜杠 307、跨块… | 0 | W1 | — |
| 921 | Tooling / environment | 新验收脚本忘了登记 selftest.sh → 验证车道拒跑、整卡 unverified 停人审（已机制化：登… | 0 | W1 | — |
| 928 | LLM provider quirks (… | env 的 -u 排在 KEY=VALUE 赋值之后会被当命令名执行（rc=127、静默空捕获） | 0 | W1 | — |
| 939 | LLM provider quirks (… | Anthropic requires consecutive tool results merged into… | 1 | W1 | — |
| 946 | LLM provider quirks (… | Gemini omits tool call IDs; system message is `systemIn… | 3 | W1 | — |
| 953 | LLM provider quirks (… | New overlay panel consumed Ctrl+C — user couldn't quit … | 2 | W1 | — |
| 960 | LLM provider quirks (… | OpenAI streaming token counts require `stream_options: … | 0 | W1 | — |
| 967 | LLM provider quirks (… | GLM (bigmodel.cn paas/v4) is streaming-tool_calls compa… | 2 | EV | 既有：glm-lane-contract |
| 974 | LLM provider quirks (… | Zhipu bills by URL, not by key: a Coding Plan key on /a… | 2 | W1 | — |
| 982 | LLM provider quirks (… | propose in print mode always errors "askuser: no callba… | 6 | FC | 无头 askFn 未接线仍成立（见 §2 FC-1） |
| 989 | LLM provider quirks (… | pricing.Cost bills input ONLY via cache-hit/miss bucket… | 3 | W1 | — |
| 996 | LLM provider quirks (… | OpenCode Go: the model list needs no key, the base URL … | 12 | W1 | — |
| 1005 | LLM provider quirks (… | Vision capability was a one-id allow-list inside pkg/de… | 4 | W1 | — |
| 1011 | Testing / CI | Switching eval off DeepSeek is a request-level claim — … | 0 | W1 | — |
| 1022 | Testing / CI | t.Cleanup(chdir) after t.TempDir() breaks Windows TempD… | 0 | W1 | — |
| 1029 | Testing / CI | Two `time.Now()` calls in the same nanosecond make Equa… | 1 | W1 | — |
| 1036 | Testing / CI | Asserting zero collisions over N random IDs is a birthd… | 0 | W1 | — |
| 1043 | Testing / CI | SSE test hangs in `srv.Close()` when the response body … | 1 | W1 | — |
| 1050 | Testing / CI | Handler parked on `<-r.Context().Done()` with an unread… | 1 | W1 | — |
| 1057 | Testing / CI | `r.FormFile` can't see multipart FIELD ORDER — walk `Mu… | 0 | W1 | — |
| 1064 | Release / upgrade | Exec-based E2E binary can't live in any test's `t.TempD… | 0 | W1 | — |
| 1073 | Release / upgrade | `tui.VersionString()` is a formatted banner, not a raw … | 1 | W1 | — |
| 1079 | Release / upgrade | Atomic self-replace requires same-filesystem temp file | 0 | W1 | — |
| 1085 | Site / landing page | `<br>` inside `<code>` survives display but `textConten… | 2 | W1 | — |
| 1096 | Site / landing page | Explicit clean-URL rewrites in `_redirects` loop foreve… | 0 | W1 | — |
| 1103 | Site / landing page | Pixel "SEEK" banner read as "98%" on mobile — font-glyp… | 0 | W1 | — |
| 1110 | Go | README hero SVG clipped on the right on GitHub — its mo… | 0 | W1 | — |
| 1130 | Go | Function-local type can't be referenced from another fu… | 0 | W1 | — |
| 1137 | Go | RWMutex not reentrant — unexported helpers can't lock i… | 0 | W1 | — |
| 1144 | Go | Adding a slash command changes `/m` prefix-match test e… | 0 | W1 | — |
| 1151 | Go | Go slice aliasing: a returned struct with a slice field… | 1 | W1 | — |
| 1158 | Go | LLM thinking-mode artifact tokens leaked into generated… | 4 | FC | post-write 过滤/CI 闸未建（见 §2 FC-2） |
| 1165 | Go | Time-dependent sort key breaks prefix-cache byte-stabil… | 4 | W1 | — |
| 1172 | Go | System prompt advertised non-existent `limit` parameter… | 15 | EV | 既有：read-whole-file / read-tail-fact / read-eof-blind |
| 1179 | Go | Permission modes should be reserved for actual permissi… | 6 | W1 | — |
| 1186 | Go | Paste-folding "any key to expand" UX shock → marker per… | 1 | W1 | — |
| 1193 | Go | reviewBranchEntry Esc checked after streaming → Esc can… | 0 | W1 | — |
| 1200 | Go | `s[:n]` on a multi-byte UTF-8 string produces broken ru… | 1 | W1 | — |
| 1207 | Go | Custom color tokens (`--dim`) must be verified against … | 0 | W1 | — |
| 1214 | Go | SVG data-URI in CSS `content:` needs explicit width/hei… | 0 | W1 | — |
| 1221 | Go | Alt+Enter steer doesn't work on macOS terminals | 0 | W1 | — |
| 1228 | Go | skill_commit must not silently pick a scope — the model… | 19 | EC | 建议孵化 skill-commit-scope-ask（见 §3.2） |
| 1235 | Go | Slash menu / status bar floated mid-screen on /clear, /… | 2 | W1 | — |
| 1242 | Go | Welcome banner pinned across the entire first turn → us… | 6 | W1 | — |
| 1249 | Go | Plan artifact write needs context BEFORE Sink.Approved … | 0 | W1 | — |
| 1256 | Go | `[plan: approved]` is a load-bearing wire-format prefix… | 10 | W1 | — |
| 1263 | Go | `/help` documented `?` hotkey not implemented; overlay … | 0 | W1 | — |
| 1270 | Go | Auto-opened picker key-passthrough allow-list is a seco… | 0 | W1 | — |
| 1277 | Go | Legacy conhost (blue PowerShell 5.x window) TUI streami… | 0 | W2 | 支持面文档化：WT 为支持宿主，不做 per-emulator 适配 |
| 1284 | Go | WM_SETTINGCHANGE broadcast blocks startup on busy Windo… | 0 | W1 | — |
| 1291 | Go | Windows CRLF paste without bracketed mode submits after… | 0 | W1 | — |
| 1298 | Go | Child process steals /dev/tty, Esc can't interrupt bash | 3 | W1 | — |
| 1305 | Go | Setsid + pipe stdout = orphan grandchildren deadlock Wa… | 0 | W1 | — |
| 1312 | Go | V4-Flash prediction returns empty content when Thinking… | 5 | W1 | — |
| 1319 | Go | Cross-compile failure: platform-specific syscall in non… | 0 | W1 | — |
| 1326 | Go | WSL sudo escapes process-group kill, bash Wait() hangs … | 2 | W1 | — |
| 1333 | Go | ObserveEnqueue goroutine captures entry parameter by re… | 2 | W1 | — |
| 1340 | Worktree / Windows | /proc/PID/children empty on CI — descendant walk return… | 0 | W1 | — |
| 1349 | Worktree / Windows | Worktree-isolated subagent edited the MAIN tree (isolat… | 0 | W1n | 修复已落地：permission.Resolve（permission.go:876）实证；词条 left-uncommitted 注记已过时 |
| 1356 | Worktree / Windows | /worktrees panel empty on Windows despite seek-managed … | 0 | W1 | — |
| 1363 | Sandbox / OS jail | never-discarded worktree refs outlive every GC (six Jun… | 0 | W1 | — |
| 1372 | Cron parser | Landlock create_ruleset returns EINVAL if you "handle" … | 1 | W1 | — |
| 1381 | Feishu / external API | `*` wildcard is not a number — must be handled before `… | 0 | W1 | — |
| 1390 | Subprocess / secrets | Feishu IM API returns HTTP 200 on business errors — mus… | 3 | W1 | — |
| 1399 | Tool output | Go's `exec.Cmd` with nil `Env` hands the child EVERY pa… | 0 | W1 | — |
| 1408 | Tool output | Head-only truncation of command output drops the verdic… | 6 | W1 | — |
| 1415 | Testing | git tool: a duplicated subcommand arg surfaces git's cr… | 9 | EV | 既有：git-subcommand-shape；词条 uncommitted 注记已被 auto-fix 卡落地 |
| 1424 | Testing | Cache-hit tests against a fake backend prove the parser… | 6 | W1 | — |
| 1431 | Tool decorators | Injecting filesystem failure for cross-OS tests: chmod … | 1 | W1 | — |
| 1440 | Tool decorators | A Tool decorator silently drops the optional interfaces… | 1 | W1 | — |
| 1447 | Filesystem / model sa… | A tool result appended on the success path vanishes whe… | 6 | W1 | — |
| 1456 | Filesystem / model sa… | `write` had no staleness protection because `edit`'s ex… | 0 | EV | 既有：write-guard-fallback |
| 1463 | Filesystem / model sa… | Check-then-write is a blind-overwrite guard with a hole… | 2 | W1 | — |
| 1470 | Filesystem / model sa… | A size+mtime freshness token misses the rename-over, wh… | 1 | W1 | — |
| 1477 | Filesystem / model sa… | Attributing a failure to the sandbox needs three gates,… | 2 | W1 | — |
| 1484 | Tooling / line endings | A read that reached EOF but got elided still vouched fo… | 4 | EV | 既有：write-elided-edit-fallback |
| 1493 | Tooling / line endings | A/B eval binaries built from the same commit share the … | 0 | W1 | — |
| 1500 | Tooling / line endings | jq `a // b as $x \| c` binds `as` INTO the `//` alterna… | 0 | W1 | — |
| 1507 | Tooling / line endings | core.autocrlf + editing .sh on Windows yields CRLF, whi… | 0 | W1 | — |
| 1514 | TUI / session stats | Hand-rolled readLine dropped bufio.Scanner's dropCR — C… | 2 | W1 | — |
| 1523 | TUI / session stats | `-resume` silently downgraded every session to the prov… | 0 | W1 | — |
| 1530 | TUI / session stats | Measure the LLM-time span at the assistant MessageEnd, … | 5 | W1 | — |
| 1537 | TUI / session stats | Windows clock granularity: `time.Since` returns exactly… | 0 | W1 | — |
| 1544 | TUI / session stats | After /compact the status bar kept showing the summaris… | 2 | W1 | — |
| 1551 | CI / gates | Flat search cost is not resumed — Tracker only re-price… | 6 | W2 | 接受的取舍：flat cost 不跨 resume，不为 $0.0007/次扩 schema |
| 1560 | CI / gates | A self-skipping test suite makes a missing CI secret lo… | 0 | W1 | — |
| 1567 | CI / gates | A benign log line sharing a prefix with a fatal one tur… | 0 | W1 | — |
| 1574 | Eval / tool descripti… | Prose rules do not stop documentation accretion — the f… | 1 | W1 | — |
| 1583 | Eval / tool descripti… | Explicit bash bans in prompts underperform tool-descrip… | 22 | EV | 既有：git-multi-query-no-bash（--list 榜首 22 分） |
| 1590 | Eval / tool descripti… | The per-turn `workflowReminder` taught `read→grep→read`… | 2 | W1 | — |
| 1597 | Eval / tool descripti… | `sysprompt.go` rule 5 ("denials → stop") discarded the … | 6 | EV | 既有：print-deny-semantics |
| 1604 | Eval / tool descripti… | Eval prompts that demand verbatim quoting silently brea… | 8 | EV | 既有：tool-selection / bash-overreach |
| 1611 | Eval / tool descripti… | Default-depth `list_dir` answers nothing about a tree —… | 7 | EV | 既有：bash-overreach（重校后同族） |
| 1619 | Build tags / dependen… | mcp.json 的键写错层级会被 encoding/json 静默忽略——A2 评估臂可无声等同 A3 | 7 | W1 | — |
| 1627 | ACP / editor & headle… | A package whose .go files are ALL tag-excluded errors o… | 0 | W1 | — |
| 1636 | ACP / editor & headle… | Session cwd has TWO resolution conventions — anchoring … | 0 | W1 | — |
| 1643 | ACP / editor & headle… | ACP session/load is an ORDER contract, and the id it lo… | 10 | W1 | — |
| 1650 | ACP / editor & headle… | Live state captured by closures must be MUTATED in plac… | 2 | W1 | — |
| 1657 | ACP / editor & headle… | Late SetPref desyncs the permission gate from the baked… | 11 | W1 | — |
| 1664 | ACP / editor & headle… | Headless ACP can't choose its provider by flag — and a … | 0 | W1 | — |
| 1671 | ACP / editor & headle… | ProjectID hashes the path STRING, not the directory — o… | 1 | W2 | 决策 c_4c6dar：ProjectID 保持纯函数（canonical-path 契约），测试钉死 |
| 1678 | ACP / editor & headle… | ACP configOptions: the dropdown exists only if session/… | 5 | W1 | — |
| 1684 | ACP / editor & headle… | http.Server.Shutdown never returns while SSE handlers a… | 0 | W1 | — |
| 1691 | ACP / editor & headle… | Subagent continuation must mint a NEW sub_sid — same-id… | 4 | W1 | — |
| 1698 | ACP / editor & headle… | 面向模型的 id 参数必须接受模型实际看得到的形态——agent 脚注只露 sub-sid 短后缀 | 6 | EV | 既有：session-id-contract |
| 1705 | ACP / editor & headle… | PRD said "sessions are archived per-project" — they are… | 2 | W1 | — |
| 1712 | ACP / editor & headle… | WebFrontend's cfgErr early-return left RPCRegistry() ni… | 3 | W1 | — |
| 1719 | ACP / editor & headle… | ACP ToolCallContent has NO shape that validates on ever… | 7 | W1 | — |
| 1728 | Eval / results data s… | Flipping loadSession on exposes every id ever handed ou… | 4 | W1 | — |
| 1747 | Eval / results data s… | Early eval result files are pretty-printed concatenated… | 0 | W1 | — |
| 1754 | Transcript analysis /… | expect.json 新增标量键撞上 `max_/min_/exact_` 界前缀扫描——每轮全红 "unk… | 1 | W1 | — |
| 1763 | Transcript analysis /… | read-argument key is schema-dependent; single-key parsi… | 14 | W1 | — |
| 1770 | Transcript analysis /… | Duplicate-fingerprint rate has two defensible denominat… | 4 | W1 | — |
| 1777 | Transcript analysis /… | A frozen baseline keeps asserting a shell-parameter sha… | 4 | W1 | — |
| 1784 | Typed agents / orches… | Committed metric snapshots must not embed absolute home… | 4 | W1 | — |
| 1793 | Typed agents / orches… | Routing built-in agent types through the custom gate si… | 0 | W1 | — |
| 1800 | Git / merge | A delete path built by filepath.Join(user input) escape… | 0 | FC | skillmgr.Uninstall 名字闸缺失（见 §2 FC-3） |
| 1809 | Git / merge | Concurrent drive merges on the SHARED main checkout sil… | 0 | W1 | — |
| 1816 | Subprocess / streamin… | Merge commits shipped with leftover conflict markers (t… | 0 | W1 | — |
| 1825 | Subprocess / streamin… | StdoutPipe forbids concurrent Wait — kill-able line-str… | 0 | W1 | — |
| 1832 | Web frontend / asset … | exec'ing an extensionless go build output fails on Wind… | 0 | W1 | — |
| 1841 | Web frontend / asset … | session/messages elements arrive as objects after r.jso… | 0 | W1 | — |
| 1848 | Web frontend / asset … | macOS bash 3.2 under set -u mangles $VAR followed by a … | 0 | W1 | — |
| 1855 | Web frontend / asset … | `${VAR-default}` under set -u passes a set-but-EMPTY va… | 1 | W1 | — |
| 1862 | Web frontend / asset … | W1.2 Host allowlist must match the host PART only — lit… | 0 | W1 | — |
| 1868 | Web frontend / asset … | A blanket "POST needs Origin" CSRF gate breaks the scri… | 0 | W1 | — |
| 1875 | Web frontend / asset … | A single-file page cannot ship bare CSP `default-src 's… | 0 | W1 | — |
| 1882 | Web frontend / asset … | A gzip middleware must both PRESERVE http.Flusher and E… | 0 | W1 | — |
| 1889 | Web frontend / asset … | http.DefaultClient silently asks for gzip and strips Co… | 0 | W1 | — |
| 1896 | Web frontend / asset … | `mktemp -t name` is BSD-only — GNU coreutils reads the … | 0 | W1 | — |
| 1903 | Web frontend / asset … | Asserting on SSE reply payloads with angle brackets fai… | 0 | W1 | — |
| 1909 | Web frontend / asset … | Store.Load returns raw un-repaired transcripts — orphan… | 2 | W1 | — |
| 1916 | Web frontend / asset … | WebFrontend.Send is not a pure broadcast — it pops the … | 0 | W1 | — |
| 1923 | Web frontend / asset … | getUserMedia needs a secure context — the documented ta… | 0 | W1 | — |
| 1929 | Web frontend / asset … | strings.Contains on a RAW JSON frame can never match a … | 0 | W1 | — |
| 1935 | Web frontend / asset … | go:embed serves the WORKING-TREE bytes — a Windows auto… | 0 | W1 | — |
| 1941 | Web frontend / asset … | web-gate 的 size 基线钉在「哪个工具链量的」上——±1.8MB 的 skew，棘轮要按 delt… | 0 | W1 | — |
| 1948 | Tooling / go list | 页面新增一个静态资产，两个既有无头验收的 scripts=1 基线同刻全断——资产数变化要顺手改全仓 scri… | 0 | W1 | — |
| 1957 | Tooling / go list | go list stops flag-parsing at the first package argumen… | 1 | W1 | — |
| 1964 | Skill stats / --idle | `git show --numstat` emits `-\t-` for binaries and one … | 2 | W1 | — |
| 1973 | MCP bridge / tool nam… | `--idle` must read last_used from the UNfiltered aggreg… | 0 | W1 | — |
| 1982 | Setup wizard / provid… | 同一份 mcp.json 两次启动可能注册出不同的工具名 | 0 | W1 | — |
| 1991 | MCP prompt section | providerOptions 扩行需三处同步——setup.go 的 envVarFor 镜像漏更会静默误触… | 0 | W1 | — |
| 2000 | DeepSeek Files API / … | JSON object key order is data — unmarshalling into a ma… | 10 | W1 | — |
| 2009 | DeepSeek Files API / … | file_id 引用参与前缀缓存——文档按「解析后的文字」进 prompt，id 本身不是 token | 2 | W2 | 读数即交付：file_id 参与前缀缓存的实测结论，无代码改动 |
| 2016 | DeepSeek Files API / … | POST /files 响应里没有 page_count / expires_at——按页计费与过期重传都拿不… | 0 | FC | 按页计费恒 $0（见 §2 FC-4） |
| 2023 | DeepSeek Files API / … | 同一份 PDF 的两次上传，解析结果并不总是完整——上传返回 id ≠ 全部页面已进上下文 | 2 | W2 | 观察记录：服务端解析完整性未定论，烟测只断言第 1 页 |
| 2031 | Lane guards / refusal… | Adding a required field to a tool schema: sibling inter… | 10 | W1 | — |
| 2040 | Distill review `s` / … | 闸的拒绝文案是给模型的指令面——写错场合等于教模型绕过 | 0 | EC | 建议孵化 deny-hint 处置回归（见 §3.2） |
| 2048 | Distill review `s` / … | 模态内异步结果消息的 stale 判定不能只带位置参数——idx 回绕后旧消息会拼接进新回合 | 0 | W1 | — |
| 2055 | Distill review `s` / … | trigram-Jaccard 阈值 fixture 手算两错同一处——先 sanity assert 再断行为 | 0 | W1 | — |
| 2062 | Distill review `s` / … | converting 吞 `n`，且「状态位当世代号」在允许退场重发后失效——风险表缓解条款即契约 | 1 | W1 | — |

（表完——267 行。分流分布：FC 5 · EC 2 · EV 13 · W1n 5 · W2 9 · W3 2 · W1 231；留观小计 247 = 267 − 5(FC) − 2(EC) − 13(EV)。）

## 6. 再生成口径（幂等验证法）

- 本文件 = §0–§4 头部散文 + §5 全量表 + 本节。表由一次性 awk+python 生成（解析逐字同 `scripts/pitfall-to-eval.sh` 的 `parse_entries`，score 同源；例外表 = §2/§3/§4 所列行号及其去向，随文保存即文档本身）。
- **幂等语义**：整体覆盖写。同一 `pitfalls.md` blob 重跑 → 输出逐字节相同（表按行号升序、无追加路径）；`pitfalls.md` 后续新增词条会使行号锚漂移——那是「下一轮 triage 全文件重生成」的预期输入变化，不是重复注记。
- 后续动作分工：FC 五张由后续开卡直接引用 §2 的行号与证据；absorbed 硬候补 0、边缘 2 条（:568/:575）由下次 archive pass 按台账 :27-33 规则人判；EC 两条由 eval 维护者用 `scripts/pitfall-to-eval.sh --make <line>` 孵化（README/prompt 草稿即起，expect.json 仍是人审闸）。
