# c_snscbi 证据：docs/pitfalls 超预算 —— 编辑瘦身

日期 2026-11-27。分支 `drive/c_snscbi`，基线 = 本卡开工时的 main（`d52e0bf8`，
卡面写的 `main@608fcf58` 之后又并入了 W6-6 `c_ekuuof` 与 W6-7 `c_w74tuo`）。
本目录的脚本都是**一次性核验工具**，只读 `git show main:<path>` + 工作树。

## 读数（scripts/weight.sh，docs/pitfalls 行）

```
开工（本分支 base=d52e0bf8，含 W6-6/W6-7 新增两条）  540425 B   over  +6925
收工（已合并 main 8b841c52，含其新增的一条条目）         521401 B   ok    （预算 533500）
本卡目标 ≤524000                                          已达成（低 2599 B）；对预算线余 12099 B
```

> 卡面给的 537035 B 是 `main@608fcf58` 的读数；分支上先后吸收了 W6-6（c_ekuuof，+1 条）
> 与 W6-7（c_w74tuo，+1 条 + 前端改动），开工实测因此是 540425 B。

## ① 吸收（口径：主题已被取代/删除）

按 `docs/pitfalls/README.md`「Live vs absorbed」逐条判定**标题主语**是不是 W6 已退役的容器
（`#bar` / `#browser` / `browser-head` / `sess-detail` / `#sess-msgs` / `#cmd-bind` /
`#cmd-projects` / `.set-card`）：

```
$ python3 - <<'PY'   # 标题里含上述 id 的 live 条目
… glob docs/pitfalls/*/*.md, 检查首行 …
PY
（输出为空）
```

**0 条**。逐条说明：`web-frontend-asset-plane/` 56 条里，只在解释里顺带提到旧容器的
（「`#sess-msgs > *` 的 (1,0,0) 盖掉 (0,2,0)」讲特异性、`lazy-resume` 里「W6 前点会话行=
只读浏览器（#browser）」讲历史形态、`空态会话头` 里 `#browser-head` 是 W6-4 期间的修复
记录）都**主题仍成立**，按 README 不算吸收。因此本卡只做编辑压缩，`absorbed/` 无新增。

## ② 编辑压缩范围与结果

- 范围：live 条目中 >1100 B 的 417 条；实际动手 **324 条**（其余要么 <=1100 B，要么已在
  2026-11-26 那轮 `c_bohzri` 压过、本轮再压会丢事实）。
- 改动条目合计 372518 → 356450 B，**压掉 16068 B（4.3%）**；以「重复叙述 / 过程铺垫 /
  Refs 解释性从句 / 垫字」为唯一压缩来源，事实 token 一条未删（见 ③）。

### 事实保留机械核（验收 ③）

`docs/evidence/c_snscbi/verify-facts.py`：对每个改动条目，比对 `git show main:<file>` 与工作树，
要求原文里**全部**行内代码 span（按 CommonMark 等长反引号配对，围栏不计）、
全部 7+ 位十六进制串、全部 `c_` 卡号在新文里逐个仍在；并检查首行 `### ` 与
Saw/Why/Fix/Lesson 五弹仍在、无 `## ` 残段。

```
$ python3 docs/evidence/c_snscbi/verify-facts.py main
OK: 324 个改动文件、4689 个事实 token 全部仍在；结构与五弹齐全
```

报告落 `facts-report.txt`（内容 `OK — 无缺失`）。

`docs/evidence/c_snscbi/audit-extra.py` 把事实面再扩一层（验收 ③ 没要求，但「删数字」同样是丢事实）：
反引号**外**的裸标识符（`policy.CWD()`、`e.Message.Content`）、带单位数值
（`280px`/`16 KiB`/`1.5MB`/`24h`）与 2 位以上整数（`32/32`/`16384`），外加「不得比原文更大 /
标题逐字不变 / 末尾换行不变 / 英文条目不得被译成中文」四项结构口径。

```
$ python3 docs/evidence/c_snscbi/audit-extra.py main
OK — 无缺失（裸标识符/数值/结构/不膨胀/语言口径全过）
```

报告落 `audit-extra.txt`。

### 过程中被这两把尺子挡回并已还原的改动（记账用）

写坏的压法被脚本抓出来后一律还原（宁可少压也不删事实）：

- 4 条英文条目被压成中文（`edit` 工具、`grep` 死锁、off-peak 窗口、pricing 缓存记账）→ 还原；
- 3 条标题被改写（`acp-ask-user…`、`agent→client JSON-RPC…`、`budget gate over a worker pool…`）→ 还原标题；
- `long-streaming-replies…` 的 Fix 段压出了嵌套反引号错乱 → 整条还原；
- `git-merge/一条目一文件的-split…`、`testing/a-test-that-builds…` 压缩后反而 +1/+2 B → 还原；
- 4 条中文条目丢了「20+ 行」「10 次调用」「2026-11-20 方案 B 线」等数值/结论句 → 补回；
- 3 条丢了裸标识符（`acpBackend.NewSession`、`settings.json`、`e.g`) → 补回；
- `deepseek-api/server-web-tools…` 曾被加进一句解释性括号（新增内容不是压缩）→ 删掉。

## ③ 验收对照

| 项 | 结果 |
|---|---|
| ① `bash scripts/pitfalls-count.sh --check` | `pitfalls-count: OK`，`live=420 absorbed=15 sections_with_entries=36`（合并 main 后含新增条目） |
| ② `bash scripts/weight.sh` 的 docs/pitfalls 行 | `521401 / 533500 ok`（不再 over）；距 524000 目标 -2599 B（已达标），距预算线 -12099 B |
| ③ 事实保留机械核 | 上面两个脚本全绿，报告 `facts-report.txt` / `audit-extra.txt` |
| ⑤ `go test ./...` | 全绿（两次，含一次冷缓存；docs 无 Go 测试，跑的是全仓回归） |
| 不动 `docs/budget.json` | 未改（抬预算线要用户签字，本卡不做） |

## ④ 未做项（车道外 / 需人）

- 抽 10 条改动最多的条目通读「五弹齐全、结论没变」：卡面标 `not-in-lane`（验证车道只跑 ③
  的机械核，人读由主会话/合并后动作补）。改动最多的 10 条（按压缩百分比）：
  `llm-provider-quirks-m6/capability-grants…`（−887 B / 24.2%）、`go/go-slice-aliasing…`（−207 / 16.7%）、
  `tooling/zero-hits-for-the-old-path…`（−217 / 13.3%）、`go/child-process-steals-dev-tty…`（−162 / 12.8%）、
  `acp-editor-headless-lanes/paseo-has-no-acp-subagent-channel…`（−189 / 12.1%）、
  `agent-loop/tools-readonlytool…`（−154 / 12.1%）、`web-frontend-asset-plane/lazy-resume…`（−115 / 11.8%）、
  `tui/status-bar-glyph-width-oracle…`（−161 / 11.4%）、`web-frontend-asset-plane/空态会话头横穿侧栏…`（−130 / 11.4%）、
  `acp-editor-headless-lanes/a-permission-popup…`（−153 / 11.2%）。
- 是否把预算线抬到新 actual+10%（533500 → 约 570000）：卡面明确留给用户签字，本卡不抬。

## ⑤ 给后续 W6-7..9 的余量口径

收工时读数 521401 B（2648 行）：
- 对预算线 533500 余 **12099 B**（≈12 KB，超过卡面要求的「给 W6-7..9 留约 10KB」）；
- 对卡面 524000 目标线已达标（低 2599 B）。`main` 期间并入的一条 W6-8 前新条目
  （`web-frontend-asset-plane/侧栏抽屉化后主区掉进第一列…`，1292 B）已计入这个余量。
若 W6-8/W6-9 各自新增 1–2 条（约 1.5–3 KB/条），余量仍在预算内；逼近 524000 时
下一张瘦身卡应按同一套两个脚本继续做，不需要抬线。
