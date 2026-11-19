# eval v2：从「单次抽样的纪律集」到「可判读的能力闸门」

**主题**：重做 eval 的**判定口径**与**覆盖面**——把每例跑一次就判 PASS/FAIL 的做法换成「N 轮 + 通过率界」，补上三条承重轴（plan / 后台子代理 / serve-web）的端到端用例，给 eval 一个夜间入口，并把能确定化的检查明确移回 Go 单测，让 eval 只承担「模型在给定 schema/prompt 下的选择倾向」这一件它真正能测的事。

**状态**：📐 设计稿（2026-11-19）。本文只设计，未动工。

**前置阅读**：[`eval/README.md`](../../eval/README.md)（判分机制与 metric 词表）、[`feature-slim-loop.md`](feature-slim-loop.md) §3（「单次红是 signal 不是 proof，判回归须双轮」——本文把它从纪律变成机制）、[`v8.md`](v8.md) 柱 T（裸核/完整消融，本文的 `--lane` 与之共用结果目录隔离）、[`../../AGENTS.md`](../../AGENTS.md) 测试节（failure-mode 清单）。

**触发起因**：v0.12.0 发版前跑全量 eval（2026-11-19，binary `11b2366`，21 例）出 3 红，逐个复核后发现**只有一个是真信号**：

| 用例 | 表面结论 | 复核结论 |
|---|---|---|
| `bash-overreach` | 红 | **真信号**：5 轮里 1 轮开局两次 `bash find`、从不调 `list_dir`（真越界）；另 5 份历史 stream 回放下全绿 |
| `code-review-effort-max` | 红 | **标定漂移**：界取自 n=3（~3k），实测 n=10 分布在 3587–14757，闸门早已在卡「详尽度」 |
| `glm-lane-contract` | 红 | **环境**：GLM Coding Plan 5 小时额度 429，与代码无关 |

一次发版前置检查里，**两个红是假警报、一个红要靠人肉复核才能定性**——这说明问题不在用例，在判定口径。

---

## 一、诊断

### 1.1 三层职责（eval 只该管第三层）

| 层 | 该测什么 | 机制 | 现状 |
|---|---|---|---|
| Go 单测（`-race`，CI 三平台） | 确定性契约：tool 输出上限、permission 决策、session 迁移、SSE/ACP 帧解析 | `go test -race ./...` | ✅ 主保障 |
| real-API e2e（CI 每日） | 依赖外部服务的确定性契约：prefix cache 真命中、files 上传往返 | `go test -run RealAPI` | ✅ 带假绿防御（必须见到 PASS 行） |
| **eval** | **模型在给定 schema/prompt 下的选择倾向** | `eval/run.sh` | ⚠️ 21 例，**不在 CI 里**，单次判定 |

### 1.2 四个具体病灶（均为 2026-11-19 实测）

- **覆盖面偏斜**：21 例的分布是 **12 例工具调用纪律**（工具选择 / 参数形状 / read offset / git 子命令形状 / write 守卫 / grep 收窄）+ 2 例评审详尽度 + 4 例权限与 CLI 契约 + 3 例杂项（指令跟随、冗余调用、schema 幻觉）。**零覆盖**：plan 状态机、后台子代理生命周期、ACP、`serve`/web 平面、MCP 路由、memory/skill 安装——恰好是 v0.11–v0.12 新增面积最大、最易被后续改动打坏的六块。
- **单次抽样**：每例跑一次即判。同一二进制 `11b2366` 上 `bash-overreach` 6 次 5 绿 1 红；`code-review-effort-max` 10 次 3587–14757（4× 离散）。单次结果既不能证伪也不能证真。
- **两个用例自认 RECORD-ONLY**，实际闸门是记录——「21 例闸门」这个说法本身虚高。
- **不在任何 workflow 里**：`grep` 全部 `.github/workflows/` 无 `eval/run.sh`。漂移只能靠人发现——`bash-overreach` 从 11-13 红到 11-18，中间被两张卡误读成回归。

### 1.3 更根本的偏差：案例来自「坑」，不是来自「能力」

用例主要由 `scripts/pitfall-to-eval.sh` 从 pitfalls 台账孵化，所以这套集本质是**历史 bug 的回归集**。它保证「别再犯同样的错」，不回答「复杂任务还能不能干成」。后者需要任务级用例，判分靠外部断言（`go build` / 判据脚本 / diff 命中），而不是数工具调用次数。

## 二、设计

### 2.1 A — 判定口径：N 轮 + 通过率界

`eval/run.sh` 新增 `--rounds N`（默认 1 = 历史行为）与 `expect.json` 的 `min_pass_rate`（缺省 1.0）。每轮写独立 JSONL 行，新增字段 `round`；判定读**本轮集合**的通过率。

三条硬约束：

1. **默认行为逐字节不变**——不传 `--rounds` 时 stdout/结果行与今天一致，照 `scripts/eval-ablate-test.sh` 的 no-env 守护测试同一口径加断言。
2. **`min_pass_rate` 是分布口径，不是放水**：`bash-overreach` 的真信号形状是「1/6 开局即 bash」，取 `min_pass_rate: 0.67`（3 轮全过才算过？否——取 2/3）仍会在连续越界时红。**取值由 E2 的实测标定表决定，不在本文拍死。**
3. **record-only 用例不进闸门**：`min_pass_rate` 缺省 + `expect.json` 标 `"record_only": true` 的用例只记录不判红（`code-review-effort-*` 归此类），runner 汇总时分列「gate 通过率」与「record 集」。

### 2.2 B — 三条承重轴用例（只进发版门禁）

| 用例 | 测什么 | 判分方式 |
|---|---|---|
| `plan-e2e` | `propose` → 用户批准 → `plan(start)` → 中途改需求 → 不重做已完成步骤 → `-resume` 后状态折叠正确 | 批准前零 write/edit（`denied_tool_ends` / `write_ok` = 0）+ 最终 transcript 里 step 顺序与 propose 一致 + resume 后不重复执行已完成步骤 |
| `subagent-bg-resume` | `run_in_background` 子代理 → 完成事件进主会话流 → `session_id` 续话 | 主会话流出现完成通知 + 续话 run 的 `resumed_from` 血统可读 + 子代理产物落在预期路径 |
| `serve-web-stream` | `seek serve --frontend=web` → `/rpc` 发送 → SSE 事件族 → 会话落盘可读 | 事件序列含 `agent_start`/`text_delta`/`agent_end` + `session/list` 能查到该会话（沿用 `scripts/serve-smoke-test.sh` 的断言形状） |

三例都**多轮、多工具**，单例成本显著高于现有集（B 项一次 $0.1–0.3），所以只在发版前跑（E4 的 release lane）。

### 2.3 C — 夜间入口

GitHub Actions `schedule` 跑 **core 子集**（约 8 例低成本 + 2 轮），上传 `trend-report.{md,json}` 为 artifact。**失败策略是「连续 3 次同向失败才 fail」**，不是每次红就红——这与 `scripts/eval-trend.sh` 已有的 `suspect:streak` 语义一致，避免夜间噪声变成告警疲劳。规则：nightly 只观测、不阻塞 PR。

### 2.4 D — 能确定化的，明确移回 Go 单测

在 `eval/README.md` 立一栏「**不由 eval 保证**」，逐例声明归属；同时补一次缺口清点（每项给出「已有测试 / 需补」结论，不预设缺口）：

- `-tools` 冻结与未知名 exit 2 → `cmd/seek/tools_flag_test.go`
- ACP `tool_call` 帧形态与 thinking chunk → `cmd/seek/acp_test.go`
- MCP `server__tool` 前缀与撞名回退 → `internal/tools/mcptool`
- session JSONL `schema_version` 与 repair → `internal/session`
- 工具输出 write-time 上限 → 各 `internal/tools/<name>`

判据：一条检查若能在**没有模型参与**的情况下断言，就不该出现在 eval 里。

### 2.5 范围之外

- ❌ 不做「全量 21 例 × 5 轮」的日常门禁——成本 $2–5/次且边际信息低；5 轮只给**已知 flaky 与承重轴**用例
- ❌ 不做 LLM-as-judge 的自由文本打分（判分必须可复算；`review_line_refs` 那类粗糙代理已是可接受的极限）
- ❌ 不引入新测试框架 / 外部依赖——仍 stdlib + `jq` + bash
- ❌ 不改 `eval/ablation/` 的隔离契约（该目录永不进 `eval/results/`）

## 三、里程碑（2h 卡粒度）

| 卡 | 内容 | 验收 |
|---|---|---|
| **E1 判定口径** | `run.sh --rounds N` + `expect.json: min_pass_rate / record_only` + 行内 `round` 字段 + 汇总分列 gate/record | 不传 flag 时输出与结果行字节等价（守护测试）；`--rounds 5` 写 5 行，`min_pass_rate: 0.6` 在 3/5 时判红、4/5 时判绿 |
| **E2 标定表** | 用 `eval/results/` 历史行 + 每例 5 轮实测，产出「每例 预期通过率 / 闸门取值 / 是否 record-only」表落 `eval/README.md`；三个已知 flaky 明确处置 | 表覆盖全部 21 例；`bash-overreach` 取到能抓住「开局即 bash」的界；`code-review-effort-*` 标 record-only |
| **E3 plan-e2e** | 承重轴用例之一：propose→批准→执行→改需求→resume | 批准前零写入断言；resume 不重跑已完成步骤；用例 README 记录 baseline 与判分口径 |
| **E4 subagent-bg-resume** | 承重轴用例之二：后台子代理完成通知 + 续话血统 | 完成通知进主流；续话 run 带 `resumed_from`；产物路径断言 |
| **E5 serve-web-stream** | 承重轴用例之三：`/rpc` → SSE → 会话落盘 | 事件序列与 `session/list` 双断言；复用 `serve-smoke-test.sh` 的启动/收尾 |
| **E6 release lane + 夜间入口** | `--lane release|core` 读名单；`ci.yml` 加 schedule 跑 core（2 轮，连续 3 次同向失败才 fail）；发版流程写进 CHANGELOG 前的检查单 | release lane 只含承重轴 3 例 + 已知稳定用例；nightly 失败策略用 `eval-trend.sh` 的 streak 口径；README 有「发版前跑什么」一节 |
| **E7 D 项清点** | 「不由 eval 保证」栏 + 五类缺口的「已有测试/需补」结论 | 每条检查有归属；发现真缺口则开独立卡，不在本卡内顺手补 |

**建议顺序**：E1 → E2（先把口径立住，否则后面所有用例都在旧口径下白标定）→ E3/E4/E5（可并行）→ E6 → E7。

## 四、测试计划（按 AGENTS.md failure-mode 清单）

E1 本身是测试基础设施，按「它会怎么坏」写：

- **取消**：`--rounds` 跑到一半 Ctrl-C → 已写入的轮次不丢、不产生半行 JSONL
- **中途中断**：某轮 seek 崩溃（无 `agent_end`）→ 该轮记为 error 而非 pass，通过率分母含它
- **畸形输入**：`min_pass_rate` 非数字 / >1 / 负数 → 启动即报错退出 2（不许静默当 1.0）
- **并发**：两个 eval 进程同时写同一结果文件 → 行不交错（`>>` 但单行写入；测试用两个进程各写 100 行验证）
- **持久化往返**：结果行新字段 `round` / `pass_rate` 可被 `scripts/eval-trend.sh` 与 `ablate.sh` 同时读（两者都有 reader 契约测试）

## 五、成本

| 场景 | 规模 | 单次成本 |
|---|---|---|
| 单轮全量（今天） | 21 例 × 1 | $0.4–1.0 |
| 发版前 release lane | 3 承重轴 + 稳定子集，各 5 轮 | $1.5–4 |
| 夜间 core | 8 例 × 2 轮 | $0.6–1.6 / 夜 |

按每月 2–3 次发版估，月成本 $25–40。**E2 的标定表必须把「每例轮数」也定下来**——便宜的读类用例可以 5 轮，承重轴 3 轮。

## 六、开放问题

1. **`min_pass_rate` 的分母**：崩溃轮（无 `agent_end`）算 fail 还是独立记 error？倾向独立记 error 且**不计入通过率**，但那样崩溃多了会「看起来绿」——需要在 E1 里定一个 `max_error_rate`。
2. **夜间跑谁的 key**：沿用 `secrets.DEEPSEEK_API_KEY`（e2e 已在用）。可选 GLM 车道用例要在额度窗口外跑（今天的 429 就是教训）。
3. **承重轴用例的判分稳定性**：`plan-e2e` 依赖模型按序调用 `plan()`，这本身也可能抽风——若实测通过率 < 0.6，说明**产品侧**的 plan 提示需要加强，而不是把界放低。这条要在 E3 的 README 里写死。
4. **是否把 `docs/prd/v8.md` 柱 T 的消融结果纳入本闸门**：消融是「裸核 vs 完整」的对照，与通过率口径正交，倾向保持独立。
