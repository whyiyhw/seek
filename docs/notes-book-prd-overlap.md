# book ↔ PRD 重复事实清单（缩并备料）

> **只盘点，不改**：本文只列「同一事实在 book 与 PRD 各定义一次」的位置、权威归属与改指针建议。
> 本轮零代码改动、零 PRD/book 改动；行号锚点均可在当前 HEAD 用 `grep -n` 复核。
> 盘点日期：2026-11-13 · 分支：`drive/c_mvv5td`

## 0. 一句话结论

共 **23 条**：**13 条冲突**（两处结论不同，读者按任一处行动会得到不同结果）、**2 条精度不同**（结论相同但一处是旧值）、**8 条一致**（可安全缩并为一处 + 指针）。

最该先修的是会直接误导读者的 8 条：**F-01**（session schema 2 vs 3）、**F-02**（Skill 5 目录 vs 3 层）、**F-05**（GC 公式漏了 updated/created）、**F-10**（cron GC 7d/24h vs 30d/14d）、**F-11**（`.malformed` 位置）、**F-14**（code-review 4 档 vs 2 档）、**F-18**（sandbox exit 1 vs 127）、**F-20**（OCR 仍存在 vs 已整体下线）。

---

## 1. 口径与权威判据

**什么算「重复事实」**：同一个可验证的常量、格式、阈值、路径、优先级或行为，在两处（或以上）被当作**事实**陈述。纯链接、纯引用、以及「见某处」不算。

**状态标记**：

| 标记 | 含义 |
|---|---|
| 🔴 冲突 | 值或结论不同；读者按任一处行动会得到不同结果。 |
| 🟡 精度不同 | 结论相同，但一处更细 / 一处是旧值，存在漂移风险。 |
| 🟢 一致 | 两处字面一致，是安全的缩并候选。 |

**权威判据（按优先级）**：

1. **运行时常量 / 行为** → 代码（`file:line`）。文档只是它的投影；表里给出可复核的代码锚点。
2. **现行设计 / 契约** → 负责该子系统的 PRD（表里注明哪一份）。
3. **历史快照** → book 章节 / PRD 验收表。它只对「某个版本发布时是什么」负责，不承担「现值」。

因此修法分两种：**book 侧通常是「加 epoch 标记 + 指针」**（它是历史书，不应被静默改写）；**PRD 侧通常是「更新现值 / 加 superseded 标记」**（它是活规格）。

**行号格式**：仓库根相对路径 `path:line`。多行用 `path:a-b`。所有位置均按当前工作树，未做任何重排。

---

## 2. 对照表

### 2.1 会话与项目指令

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-01 | `session` JSONL header 的 `schema_version` 当前值 | `docs/book/chapter-09.md:259`（`CurrentSchemaVersion = 2`） | `docs/prd/v1.md:169`、`docs/prd/feature-subagent.md:125`、`docs/prd/feature-vision.md:72` 均写 2；`docs/prd/feature-suggested-reply.md:199` 记 2→3 | 🔴 | 代码 `internal/session/session.go:51`（=3）；变更记录 `docs/prd/feature-suggested-reply.md:199` | book §9.4 加「写作时=M5.1，值 2；v4 起 3」的 epoch 注；三份 PRD 的字面 `2` 改为「见 `session.CurrentSchemaVersion`（v4 起 3）」。v4 PRD 保留 2→3 的变更记录，不回改。 |
| F-02 | Skill 扫描目录与优先级层数 | `docs/book/chapter-11.md:174-186`（5 处 / 「4+1」）、`docs/book/chapter-11.md:472` | `docs/prd/v2.md:294`（project `.seek` > user > builtin，3 层）、`docs/prd/notes-memory-skill-gap.md:71`；反证 `docs/prd/v2.md:302`（验收 #12 仍要求 `.claude/skills` 测试全绿） | 🔴 | 代码 `internal/skill/loader.go:43-48`（project `.seek` + `~/.seek` + builtin） | book §11.3 开头加「v0 快照；v2 起收敛为 3 层，见 PRD v2 §7 #5 / 第 17 章」；若保留 5 目录表，明确标「v0 历史行为，`.claude` / XDG 槽位已移除」。v2 保持不动。 |
| F-03 | `AGENTS.md` 向上搜索 5 层、单文件硬上限 64 KB | `docs/book/chapter-11.md:327-331`、`docs/book/chapter-11.md:391-402` | `docs/prd/v0.md:359-361` | 🟢 | 代码 `internal/projectmd/projectmd.go:33`（`maxAscend = 5`）、`internal/projectmd/projectmd.go:41`（`maxBytes = 64 * 1024`） | v0 保留规格；book §11.6 删掉重复的 const 数值，改成一句「5 层 / 64 KB，规格见 `docs/prd/v0.md:359-361`；实现见 `projectmd.go`」。 |

### 2.2 Skill 与工具契约

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-09 | `.stats.jsonl` 单次 `write(2)` + 行 payload < `PIPE_BUF`（4 KiB）保证 append 原子性（具体 3 KiB 守门只出现在 book/code） | `docs/book/chapter-17.md:258-268`、`docs/book/chapter-17.md:451` | `docs/prd/v2.md:297`（验收 #8）、`docs/prd/v2.md:333`（风险表） | 🟢 | 代码 `internal/skillstats/skillstats.go:88`（`len(line) > 3072`） | v2 §7 #8 / §9 风险表补一句「具体守门 = 3072 bytes，见 const」；book 保留 POSIX 原子性的教学解释，但把不变量本身指向 v2，避免两处各讲一遍。 |
| F-23 | `read` 工具对 ≤32 KiB 文件整体返回 | `docs/book/appendix-b.md:49`（「≤32 KiB 的小文件整体返回、`limit` 默认 200」） | `docs/prd/feature-webfetch.md:31`（对比表 read 列：「≤32 KiB 整体返回 / 64 KiB 结果上限」） | 🟢 | 代码 `internal/tools/read/read.go:56`（`defaultWholeReadBytes = 32 * 1024`）；详细契约 `docs/test-plan-read-tool.md` | `feature-webfetch` 的对比表改为「见 `docs/test-plan-read-tool.md`」——webfetch PRD 不该维护 read 的 cap。book 已经指向该 test plan，保持。 |

### 2.3 Memory

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-04 | M-index 注入预算 1500 token；截断排序 `pinned→recall_count→name` | `docs/book/chapter-16.md:397`、`docs/book/README.md:210` | `docs/prd/v1.md:780`（1500 + 排序）；反证 `docs/prd/v1.md:165`（同一「注入到 prompt 的索引」预算的旧值 `~3k tokens`） | 🟡 | 代码 `internal/memory/tokens.go:18`（`maxMIndexTokens = 1500`） | `docs/prd/v1.md:165` 改为 1500 并指向 `tokens.go`；book:397 保留 1500 但标「见 `docs/prd/v1.md:780`（M5.15）」；`book/README.md:210` 只留章节链接，不再复述数字。 |
| F-05 | M 层衰减公式的 `lastActive = max(created, updated, last_recalled)` | `docs/book/chapter-16.md:317`（只写 `LastRecalledAt`） | `docs/prd/v1.md:545-548`（三值 max） | 🔴 | 代码 `internal/memory/score.go:89-95`；v1 公式正确 | book:317 改为三值 max（或删公式、只引 `docs/prd/v1.md:540-548`（§遗忘））；v1 保持。 |
| F-06 | 项目目录名 `sha256(absPath)[:16]`；`.seek/project-id` 可覆盖 | `docs/book/chapter-16.md:61`、`docs/book/chapter-16.md:343`、`docs/book/chapter-16.md:362` | `docs/prd/v1.md:114`、`docs/prd/v2.md:152`、`docs/prd/feature-plan-mode.md:378`（其中 `feature-plan-mode` 附带的实现位置声明已过期） | 🟡 | 代码 `internal/paths/paths.go:146`（`ProjectID` 已在 `internal/paths`） | `v2.md:152` / `feature-plan-mode.md:378` / `book:343` 统一指向 `paths.ProjectID`；`feature-plan-mode.md:378` 的「当前实现在 `internal/memory`（私有函数），建议提升到 `internal/paths`」已过期，改为「已落 `internal/paths`」；`docs/prd/v1.md:114`（§M）保留值的定义。 |

### 2.4 成本与预算

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-07 | budget 阈值 `Warn=0.60` / `Critical=0.75`（旧 0.80/0.95） | `docs/book/chapter-13.md:266-267`、`docs/book/chapter-13.md:292-304`、`docs/book/chapter-15.md:525` | `docs/prd/v0.md:726` | 🟢 | 代码 `internal/budget/budget.go:74-75`；v0 是「80→60 / 95→75」的变更记录 | v0 保留历史变更行；book ch13 的常量改为引 `budget.go`，只保留「为什么是 60/75」的论证（book-only 价值）。 |
| F-08 | DeepSeek **现行**定价表数值（book 用 2026-08 的「当前」表述） | `docs/book/chapter-13.md:149-159`（2026-08-16 峰价：V4-Flash 0.44/0.014/1.32；V4-Pro 1.32/0.044/3.96） | `docs/prd/v0.md:540-543`（2026-05 促销快照：0.14/0.0028/0.28；0.435/0.003625/0.87；并注明「现行费率以 `pricing.go` 为准」） | 🔴 | 代码 `internal/pricing/pricing.go:76-79`（V4.1-Flash，2026-09-10 起 0.30/0.006/1.20；错峰半价） | book 表头标「2026-08 快照；现行见 `pricing.go`；2026-05 历史见 `docs/prd/v0.md:540-543`」；v0 保持历史快照 + 指针；不要保留 book 里「当前定价」的措辞。 |

### 2.5 Routines

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-10 | cron `runs/` 保留 100 条 + 7 天；`.malformed` 保留 24h | `docs/book/chapter-22.md:177-181` | `docs/prd/feature-routines.md:317-318`（现行：runs 100 + 30d；malformed 20 + 14d）；反证 `docs/prd/feature-routines.md:511-512`（§8 旧叙事仍是 100+7d / 24h） | 🔴 | 代码 `internal/routines/gc.go:25-41`（100/30d；20/14d） | book ch22 更新为 100+30d、20+14d（或只写「见 `docs/prd/feature-routines.md:309-318`（§3.10）」）；`feature-routines.md` §8 标「G4+G5 交付叙事，默认值以 §3.10 为准」并删掉旧数字。 |
| F-11 | 畸形 trigger 文件移到 `~/.seek/cron/.malformed/` | `docs/book/chapter-22.md:82`（布局树）、`docs/book/chapter-22.md:100` | `docs/prd/feature-routines.md:58-59`（布局树只列 `triggers/`）、`docs/prd/feature-routines.md:318`（`triggers/.malformed/<id>.json`）、`docs/prd/feature-routines.md:489` | 🔴 | 代码 `internal/routines/tick.go:335`（`triggers/.malformed`） | book 布局树改成 `triggers/.malformed/`；`feature-routines.md` §3.1 的树补上该子目录，避免下一份文档再抄错。 |

### 2.6 Subagent

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-12 | `subagent_type` = `general-purpose` / `explore` / `plan`；`plan` 与 `explore` 共用只读工具子集 | `docs/book/chapter-21.md:278-284` | `docs/prd/feature-subagent.md:63`（enum）、`docs/prd/feature-subagent.md:196-202`（模板表）、`docs/prd/feature-subagent.md:209`（`plan ≡ explore`） | 🟢 | 代码 `internal/subagent/types.go:47-60`；PRD §3.6 | book 保留三行表作教学，但加一句「值集合以 `types.go` / PRD §3.6 为准」；若要缩并，book 改成两句 prose + 指针。 |
| F-13 | 子代理 summary 上限 `MaxSummaryBytes = 4096`（提示词写 ~4000 字符） | `docs/book/chapter-21.md:316`（「4 KB 的 summary」） | `docs/prd/feature-subagent.md:108`、`docs/prd/feature-subagent.md:219` | 🟢 | 代码 `internal/subagent/wireformat.go:19`；PRD §3.2 | book:316 保留「4 KB」量级，补 `MaxSummaryBytes` 名字 / 指针；PRD 保持唯一详细定义。 |

### 2.7 v0.7 单点工具

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-14 | code-review effort 档位数（4 档 vs 2 档） | `docs/book/chapter-23.md:85`（四档 quick/medium/high/max；PRD 记录的原设计是 low/medium/high/max）、`docs/book/chapter-23.md:92`、`docs/book/chapter-23.md:102`、`docs/book/chapter-23.md:106` | `docs/prd/feature-code-review.md:5-6`（已收敛 2 档 quick/thorough，旧 4 名软别名）、`docs/prd/feature-code-review.md:97`（D4 定稿）；反证 `docs/prd/README.md:26`（仍写「4 档 effort framing」） | 🔴 | 代码 `internal/skill/builtin/code-review.md:8-11`、`internal/skill/builtin/code-review.md:30-40`（2 档 + 旧名别名） | book §23.3 改为 2 档（quick/thorough），把「4 档为何收敛」作为 eval 教训保留；`docs/prd/README.md:26` 改为「2 档（旧 4 名别名）」；`book:106` 的「eval 验证 low 与 max 有差异」也要改——PRD D4 的结论是分不开、降级 record-only。 |
| F-15 | `/review` 别名映射到哪个默认档 | `docs/book/chapter-23.md:96`（`/review` = `/code-review quick`） | `docs/prd/feature-code-review.md:5`（status：quick）vs `docs/prd/feature-code-review.md:68`（D3 推荐 medium）、`docs/prd/feature-code-review.md:242`（`cmdReview` 传 `"medium"`） | 🔴 | 代码 `internal/skill/builtin/code-review.md:9`（`/review` = quick）；PRD status 行 | `feature-code-review.md` D3/§5.4 补「最终实现改为 quick（见 status 行）」，不要改写决策史；book 保持 quick 并引 status。 |
| F-16 | 后台 bash 在 Windows 的进程树终止机制 | `docs/book/chapter-23.md:144`（`cmd.exe /C` + 进程组 handle 跟踪） | `docs/prd/feature-bash-monitor.md:205`（`taskkill /T /F /PID` + `HideWindow`；明确不用 Job Object） | 🔴 | 代码 `internal/tools/bash/bash_windows.go:18-34` | book:144 改为「`cmd.exe /C` + `taskkill /T /F /PID`（按 PID 树终止；`HideWindow` 防闪窗）」；PRD 保持。 |
| F-17 | 交互回合 `session.completed` 推送默认阈值 60s、0 关闭 | `docs/book/chapter-23.md:234-236` | `docs/prd/feature-mobile-push.md:190` | 🟢 | 代码 `internal/config/config.go:184-188`（nil→60）；PRD §9（`docs/prd/feature-mobile-push.md:184-199`） | book 保留用户可见行为（60s / 0 关），补「默认值见 `SessionNotifySecondsOrDefault`」；PRD 保持唯一规格。 |

### 2.8 v0.8 四柱

| ID | 事实 | book 位置 | PRD 位置 | 状态 | 权威 | 缩并建议（另处改指针） |
|---|---|---|---|---|---|---|
| F-18 | Linux landlock 加不上时 fail-closed 的退出码 | `docs/book/chapter-24.md:159`（写「exit 1」） | `docs/prd/feature-sandbox.md:5`（「退出 127」） | 🔴 | 代码 `internal/sandbox/sandbox_linux.go:81`（`applyLandlock` 失败 → `os.Exit(127)`；同一 trampoline 的其它 fail-closed 路径 `internal/sandbox/sandbox_linux.go:72`、`internal/sandbox/sandbox_linux.go:77`、`internal/sandbox/sandbox_linux.go:91` 也是 127）；内核无 landlock → `Available()=false` 降级，不进 trampoline | book:159 改为 127，并区分「不支持→降级」与「已进 trampoline 但加不上→127」；PRD 保持。 |
| F-19 | 沙箱允许集合（写白名单 + 网络开关 + 作用域；同一 allowlist 的三个轴） | `docs/book/chapter-24.md:134-135`（write 仅 worktree + `/tmp`；network 默认禁）、`docs/book/chapter-24.md:187` | `docs/prd/feature-sandbox.md:50-53`（写 project root + worktrees + `$TMPDIR` + `~/.seek`；网络默认拒但放行 LLM endpoint + webhook host） | 🔴 | 代码 `internal/sandbox/sandbox.go:24-27`（`AllowNetwork bool`，无 host allowlist）+ `cmd/seek/main.go:558-560`（只沙箱 bash 子进程；`WritableDirs: []string{wt}`；未开 `AllowNetwork`） | PRD D3 改为「MVP：bash 子进程；写 = worktree + tmp；网络 = 布尔全禁，无 host allowlist」，把「放行 LLM/webhook」标为未实现 / 后续；book 补「仅 bash 子进程（seek 自身的 API 调用不在牢内）」的范围限定。 |
| F-20 | 柱 Q OCR 引擎与生命周期（book 的 v0.8 现值 vs PRD 的 v0.9 后现值；时序冲突） | `docs/book/chapter-24.md:294-309`（三种引擎：macOS Vision / Linux-Windows `ocr.command`；Swift 编译缓存） | `docs/prd/feature-vision.md:10`（OCR 整体下线）、`docs/prd/feature-vision.md:14-22`（前提变了）、`docs/prd/feature-vision.md:150-159`（M-V.0 删除 `internal/ocr` / Swift 助手）、`docs/prd/feature-vision.md:210`（2026-08-22 决策）；历史规格 `docs/prd/archive/feature-image-ocr.md` | 🔴 | `docs/prd/feature-vision.md`（后发决定）+ 代码（`internal/ocr` 已不存在；检测迁 `internal/imgrefs`） | book §24.5 开头加「后记：v0.9 起柱 Q OCR 已整体下线，图片走原生视觉；见 `docs/prd/feature-vision.md:208-212`（§7.1）」；保留 v7 历史叙述；PRD 保持权威。 |
| F-21 | ACP 传输 / 方法集 / MVP 限制 | `docs/book/chapter-24.md:207`（方法）、`docs/book/chapter-24.md:248`（newline JSON-RPC）、`docs/book/chapter-24.md:254-257`（无审批 / 无 resume / 无 slash / 短暂存） | `docs/prd/feature-acp.md:5`（实现：newline-delimited + `initialize`/`session/new`/`session/prompt`/`session/update`）、`docs/prd/feature-acp.md:42-48`（方法映射）、`docs/prd/feature-acp.md:9`（剩余 backlog：`request_permission`、`session/load`、多 session、slash、版本协商） | 🟢 | `docs/prd/feature-acp.md:5-9`（status）+ 代码 `internal/acp/server.go:162-177`（方法 dispatch） | book 保留一段式概述；「还剩什么没做」指向 `docs/prd/feature-acp.md:5-9`，不在 book 里做长期清单；PRD 保持。 |
| F-22 | Autopilot 闭环阶段顺序（per-task commit 在聚合前）与 commit msg | `docs/book/chapter-24.md:43`（写成「聚合→本地 commit」，顺序相反）、`docs/book/chapter-24.md:86`（正确：聚合前 per-task commit；msg `autopilot: <title>`） | `docs/prd/feature-autopilot.md:9`（per-task commit msg）、`docs/prd/feature-autopilot.md:27`（各自本地 commit→聚合摘要→push）、`docs/prd/feature-autopilot.md:134-137`（fan-out 内每任务 commit，再 aggregate） | 🔴 | `docs/prd/feature-autopilot.md:9` + `docs/prd/feature-autopilot.md:70-86`（§D1）+ 代码 `internal/autopilot` | book:43 改为「fleet（每任务本地 commit）→ 聚合 → 报告」；book:86 保留；PRD 保持。book 内部这两句当前互相矛盾，修 book 时一并统一。 |

---

## 3. 缩并顺序建议

| 批次 | 条目 | 理由 |
|---|---|---|
| **P0 · 先修冲突** | F-01、F-02、F-05、F-10、F-11、F-14、F-18、F-20 | 读者按 book 或 PRD 任一处行动会得到不同结果；其中 F-20 是「功能已下线但书里仍写成可用」的误导。 |
| **P1 · 修旧值 / 精度** | F-04、F-06、F-08、F-15、F-16、F-19、F-22 | 结论能对上，但有一处是旧值或实现细节不准确；不改不会立刻坏事，但下一次引用会继续扩散。 |
| **P2 · 安全缩并** | F-03、F-07、F-09、F-12、F-13、F-17、F-21、F-23 | 两处一致，属于「同一事实写两遍」；删一处、留指针即可，行为不变。 |

**一个通用动作**：凡「运行时常量」类事实（F-01/F-04/F-05/F-07/F-08/F-09/F-10/F-13/F-18），PRD/book 都不应复制数值，应复制**常量名 + 代码锚点**；数值只在代码里出现一次。这能一次性消掉这一整类漂移。

---

## 4. 抽查记录（验收要求 5 条）

以下 5 条按「book 陈述 → 代码现值」实测；命令可在当前 HEAD 直接复跑。

| # | 条目 | book 原文 | 代码实测 | 结论 |
|---|---|---|---|---|
| 1 | F-01 | `docs/book/chapter-09.md:259`：`CurrentSchemaVersion = 2` | `grep -n "CurrentSchemaVersion = " internal/session/session.go` → `51:const CurrentSchemaVersion = 3` | ✅ 属实（book 旧值） |
| 2 | F-10 | `docs/book/chapter-22.md:179-180`：`runs/` 100 条 + 7 天；`.malformed` 24h | `grep -n "DefaultRunsKeepRecent\|DefaultRunsMaxAge\|DefaultMalformed" internal/routines/gc.go` → `25:100`、`30:30 * 24 * time.Hour`、`36:20`、`41:14 * 24 * time.Hour` | ✅ 属实（book 抄了 PRD §8 旧叙事，§3.10 已更新） |
| 3 | F-14 | `docs/book/chapter-23.md:85`：四档 effort（quick/medium/high/max） | `grep -n "quick\|thorough" internal/skill/builtin/code-review.md` → `8: [quick\|thorough]`、`30: quick (default)`、`33: thorough`、`38-40: 旧 4 名映射` | ✅ 属实（book 仍是 4 档叙事） |
| 4 | F-18 | `docs/book/chapter-24.md:159`：landlock 创建失败 `exit 1` | `grep -n "os.Exit(127)" internal/sandbox/sandbox_linux.go` → `72,77,81,91`（81 行注释 `fail closed — do NOT run unconfined`） | ✅ 属实（退出码写错） |
| 5 | F-02 | `docs/book/chapter-11.md:176`：「Skill 文件可以放在 5 个地方」 | `sed -n '43,48p' internal/skill/loader.go` → 注释只列 `<project>/.seek/skills/`、`~/.seek/skills/`、`embedded builtin` 三处 | ✅ 属实（book 的 `.claude` / XDG 槽位已不在 loader） |

> 其余 18 条的锚点已在表中给出；复核方法与上表相同：先 `grep -n` 定位，再 `read`/`sed -n` 读 5–10 行上下文。

---

## 5. 未纳入 / 范围说明

- **`docs/book/chapter-19.md`、`chapter-20.md` 是占位章**（仅「内容预告」，各约 3 KB），没有可与 PRD 对表的实质事实；本轮不强行凑条目。
- **`docs/prd/archive/feature-image-ocr.md`** 是柱 Q 的历史规格，已被 `feature-vision.md` 取代；只在 F-20 里作为历史来源引用，不作为活 PRD 参与对表。
- **`docs/guide-*.md`、`docs/comparison.md`、`docs/pitfalls.md`** 不在本卡范围（卡面限定 `docs/book` × `docs/prd/*`）。它们也携带同一批事实的副本，例如 `docs/guide-budget.md:33-36` 重复了 60/75；若后续扩大范围，可按同一张表继续收编。
- **`docs/prd/README.md`** 是索引而非规格；只在 F-14 里作为「旧措辞仍在流传」的证据引用。
- 本轮**未改** book、PRD、代码；本文件之外不应有其它改动（本轮只新增本文件）。

---

## 6. 附：盘点顺带发现（不计入上表）

以下不是 book ↔ PRD 的重复，但会在同一轮缩并里遇到，先记一笔：

1. **book 内部不一致**：`docs/book/README.md:190` 写「16.4 调用面:四个工具 + 两个命令」，而 `docs/book/chapter-16.md:178` 写「五个工具」。同一本书的目录与正文章节数不一致；修 book 时应顺手统一。
2. **PRD 内部残留**：`docs/prd/feature-code-review.md` 的 status（`docs/prd/feature-code-review.md:5-6`，2 档）与 §2.1/§5.1 的旧文案（`docs/prd/feature-code-review.md:32`、`docs/prd/feature-code-review.md:204`，仍写 `[low|medium|high|max]`）并存；F-14/F-15 只覆盖 book↔PRD 的对照，PRD 内部的旧参数串需要在同一轮清掉。
3. **PRD ↔ 代码的相邻偏差**：`docs/prd/feature-autopilot.md:83` 写「max 子代理数（默认 8，复用 subagent 既有上限思路）」，而 `docs/prd/feature-subagent.md:478` 与 `internal/subagent/manager.go:25` 的默认值是 3。二者可能是「fleet 任务上限」与「subagent Manager 并发上限」两个轴，但 PRD 没有区分；建议缩并时显式命名两个 cap，避免读者把 8 当成 subagent 的默认值。

---

*本文件为文档盘点产物；零代码改动、零 PRD/book 改动。*
