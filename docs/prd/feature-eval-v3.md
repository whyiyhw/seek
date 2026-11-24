# eval v3：判分层与外部锚——从「边界纪律」到「质量面 + harness 净值」

**主题**：给 eval 补两块 v2 明确留白的面——**J 线（判分层）**：用带校准的 LLM-as-judge 对每轮已落行的 `final_text` 判「答案对不对」，补机械指标够不到的质量面；**A 线（外部锚）**：自建 Terminal-Bench-lite 任务集，量出「seek 这个 harness 在固定任务集上的解出率」，并提供同任务同模型跨 harness 的私有对照协议。J 线修订 v2 §2.5 的「❌ 不做 LLM-as-judge」条款；A 线受 #2575「不做公开跑分」约束，框成**私有外部校准**，结果永不外发。

**状态**：📐 设计稿（2026-11-22）→ **J1–J3 与 A1–A3 已全部落地**（2026-11-22，用户主会话直接实施，无卡；提交 8ee2738 / 4680b8a4 / 454d77f5 / b808469c / 7954771e）。落地注记：① J1 判分层按 §2.1 全量落地——judge.py 单发 + judge 块校验 + 三键 data-gated + gate/record 染色 + kill-switch；既有四个字节 pin 自测回归绿（不判分行字节不变）。② J2 校准集实落 **34 条**（27 真实 + 7 构造），标签为**代标初稿、待用户抽查**（本 PRD §2.1 的口径）。**真车道标定已完成**（2026-11-23，五轮迭代，提交 0c393911）：kimi-k3.1-pro @ opencode 订阅道（登录透传——本机无 OPENCODE_API_KEY，凭证路径与官方重跑口径的差异是已知留白），末轮 **30/34 = 0.88 agreement、false-pass = 0 → gate-eligible**（报告 `eval/judge-calibration/report.md`）。标定过程改判三条代标（judge 对、代标错——预览截断误标，证据入 label_note）并落地四个 judge 基建修复（见 0c393911 提交说明）；**gate 已开**（2026-11-24 用户拍板「都开卡」＝开 gate，卡 c_xhbqae）：`code-review-effort-low` / `read-tail-fact` 的 `judge.mode` 由 record 切到 gate——开关就是该 per-case 字段（`read_gate_config` 校验），未另造全局开关；`SEEK_EVAL_JUDGE=0` 一行关回（可逆；三键 data-gated 口径不变，关后行字节回到 judge 层引入前形状）。开 gate 时两个保留理由照旧成立、留档非清零：①单轮余量薄（30/34 = 0.88；少一条同意行 0.853 已贴 0.85 线，再少一条 0.824 跌破）；②凭证路径非正式口径（登录透传，本机无 `OPENCODE_API_KEY`）。③ J3 首批接线两例（code-review-effort-low / read-tail-fact）原为 record，现均 gate；前者仍 `record_only: true`——judge 红染行上 `failures` 但不染 exit。④ A1 八题全 record_only 起步，任务自验 8/8 原始红 + 8/8 参考解绿；首轮真 key 基线后按区分度调题（§六.3）。⑤ A3 的 anchor-crossharness.sh 内建命令形状**首次真用需校准**（本机无 opencode CLI，降级路径已验；双向烟测用假 harness 验过 solved 布尔两个方向）。⑥ 随线产品修复（c10b589a）：`--no-save` 铸一次性会话 id，修 OAuth 登录道 headless 的 x-opencode-session 400（判分冒烟抓出；有 OPENCODE_API_KEY 的 key 道不受影响——解释了既有 eval 行全绿而登录道 headless 崩）。**修订依据**（原状态段保留于下）：v2 §2.5「判分必须可复算」的立场由用户 2026-11-22 拍板修订——判分主体仍须可复算（确定性优先、judge 只补复算不可能的面），但「可复算性」的落点从**单次判决**移到**校准层**：标注集与 agreement 报告入库可复算，判决本身是分布口径的一个样本（与 E1「单次红是 signal 不是 proof」同构）。另随本 PRD 收口 v2 状态头的一处漂移：E3（plan-e2e）实际已落地（c_c5wwey，2026-11-21，见 `eval/README.md` 承重轴节与 `eval/cases/plan-e2e/`），v2 状态头「其余里程碑（E3、E7）未动工」中 E3 半句作废（E7 仍未动工）。

**前置阅读**：[`eval/README.md`](../../eval/README.md)（判分机制 / metric 词表 / E2 标定表）、[`feature-eval-v2.md`](feature-eval-v2.md)（§2.5 被修订条款、E1 判定口径）、[`v8.md`](v8.md) §9.1（key 与沙盒纪律——judge 与锚点全部继承）、`internal/verifier/judge.go` 与 `internal/goal/judge.go`（仓内两个 judge 先例：fresh-context 单发、JSON-only、no-JSON=fail）。

**触发起因**：2026-11-22 对 eval 体系做外部对照（Anthropic《Demystifying evals for AI agents》等），确认两个结构性空白：①每轮 `final_text`（run.sh 落行、截 2000 字，注释自称供 offline gold-answer scoring）**至今零闸门消费方**——答案正确性、是否答非所问，34 例无一能测；②全部用例是自我一致性回归网（pitfalls 孵化），答不了「harness 本身值多少分」。用户拍板：两个都要补。

---

## 一、诊断

### 1.1 J：质量面无防

- 机械指标的天花板：`runner_assert_failures` / `review_line_refs` / 各 `*_calls` 数的是**形状**，不是**内容对不对**。`read-tail-fact` 这类用例的机械绿只证明「读到了尾部」，不证明「报的数字是对的」。
- `final_text` 已落行但无消费者（grep 全仓：仅 ablation 报表与 provider 自测的 contains 断言）。质量面 = 盲区。
- 行业共识（Anthropic 2026-01）：确定性判定优先，LLM-as-judge 只补灵活性面，且 judge 必须**先对人校准**、带「判不了」逃生口（doubt/Unknown），否则宁可不判。

### 1.2 A：只有自我一致性，没有外部锚

- 34 例 + 3 承重轴全部回答「自己相对自己没退步」。跨 harness 净值（同模型下 seek 相对其它 harness 加多少分）无任何数据源。
- 约束：#2575（2026-11-12 用户决定，4 处文档引用）禁止公开跑分 → 锚点只能是**私有校准**：任务集与结果入库，永不发布、不做 leaderboard。
- SWE-bench Verified 已饱和并被 OpenAI 弃用（2026-02，污染 + 测试质量）；Terminal-Bench 真接入要 Docker/Harbor 基建。自建轻量任务集（自含 fixture + 机械 check）是唯一同时满足 hermetic、零新依赖、#2575 的形态。

## 二、设计

### 2.1 J — 判分层

**调用形状**（照 `internal/verifier/judge.go` 模子）：`eval/tools/judge.py` 组装 prompt（系统段 + per-case rubric + 原任务 prompt + **标记为 DATA 的 final_text**，防提示注入），驱动 `$SEEK_EVAL_BINARY -yolo -no-save -json <lane args> -model <judge model>` 一次单发，join 非推理 `text_delta` 得判决 JSON。判决契约 `{"pass": true|false|null, "reason": "..."}`：**null = doubt**；解析失败 / 超时 / 非零退出一律归 doubt——**永不假绿**。judge 与被测同走一条 provider 车道（默认 opencode 订阅），凭证 env 原样继承，零新增鉴权代码；判分模型默认**跨家族**（`kimi-k3.1-pro`，回退 `glm-5.3`，`SEEK_EVAL_JUDGE_MODEL` 可换）——judge 与被测不同家族，压自宠偏置。

**接入**（最小扰动）：

- `expect.json` 新增 opt-in 块：`{"judge": {"rubric": "...", "mode": "record"|"gate", "model": "..."}}`；`read_gate_config` 扩展校验（畸形 → 启动 exit 2，同 `min_pass_rate` 纪律）。
- `run_one` 在 `final_text` 提取后调 judge；行上 **data-gated** 三键 `judge_verdict`（"pass"|"fail"|"doubt"）/ `judge_reason`（截 ~300 字）/ `judge_model`——不判分的行字节不变，v2 的三个字节等价 pin（ablate no-env blob、lane-test 白名单、rounds-test ①）不需动。
- 染色规则：`mode=gate` 且 verdict=fail → 进 `failures[]`；**doubt 与 record 模式永不染色**。全局 kill-switch `SEEK_EVAL_JUDGE=0`；`final_text` 为空跳过判分（键不出现）。
- `elapsed_s` 只量被测调用——judge 在 elapsed 收口之后跑。

**校准（J 的合法性来源，对齐 E2 文化）**：`eval/judge-calibration/fixtures.jsonl` ≥30 行，从已入库 results 的 `final_text` 抽样（机械绿 / 机械红 / 崩溃形 / 模糊形四类都要有），**代标初稿 + 抽查**口径（2026-11-22 用户拍板）；`eval/tools/judge-calibrate.sh` 跑全集算 agreement、混淆矩阵、**分方向错误**（false-pass——「烂答案判过」——是危险方向，单列）。**开 gate 资格**：总 agreement ≥85% 且 false-pass=0；达标前一切 judge 接线 record_only。

### 2.2 A — 外部锚（Terminal-Bench-lite）

**形态**：锚点任务落成**普通 eval cases**（`eval/cases/anchor-<name>/`），复用全部既有机制，零 `run.sh` 改动：

- `prompt.txt`（自含任务书）+ `workspace/`（fixture 起点）+ `check.sh`（纯机械验证，exit 0 = solved）+ `README.md`（能力来源与设计理由）+ `expect.json`（record_only 起步）。
- `runner.sh`（plan-e2e 模式）：自建 scratch（`eval/tmp/anchor-*/{home,ws}`，独立 SEEK_HOME），复制 workspace 进 ws，**cwd=ws** 调 `"$SEEK_EVAL_BINARY" -yolo -no-save -json ${SEEK_EVAL_PROVIDER_ARGS-} -p …`，收尾在 ws 里跑 `check.sh`；未解出发 `{"type":"runner_fact","assert":"anchor_solved","failed":true}` → 既有 `runner_assert_failures` 指标 + `max_runner_assert_failures: 0` 直接闸门；rounds / 车道 / trend / streak 全部白拿。
- **8 题任务集**（无网络、无 Docker，只用 go/python/bash/jq/git；难度目标 = flash 解得出一部分、解不全，有区分度）：①修失败测试的微型 Go 仓 ②CSV 清洗管道（diff 黄金输出）③按 spec 写 Python CLI ④约束重构（公共 API 不变 + 黄金测试）⑤微妙 bug 调试（时区 / off-by-one）⑥文档矛盾调和（grep 不变式）⑦配置 v1→v2 迁移（jq 断言）⑧git 考古（定位引入某字符串的 commit）。
- `eval/lane-anchor.txt` 车道名单（8 题全进）。
- **跨 harness 对照**落 `eval/anchor/`（新目录，与 `eval/ablation/` 同纪律——**永不进 `eval/results/`**，否则在 trend 里伪造回归）：协议文档 + best-effort 脚本驱动 **opencode CLI**（同订阅、同模型 → 唯一被隔离变量是 harness）跑同一批任务、同一 `check.sh` 判分；环境无 CLI 则降级为手工协议。#2575 边界写进文档头：仅私有校准，不发布。

### 2.3 范围之外

- ❌ 不做公开跑分 / leaderboard / 对外发布任何锚点数字（#2575）
- ❌ 不真接 Terminal-Bench / Harbor / SWE-bench（Docker 基建 + 外部可比性收益不抵，自建集先走）
- ❌ 不给 34 例全量接 judge——只接质量面用例（首批 2 例），边界纪律用例的机械界已够
- ❌ judge 不进 trend 新列、不进夜间 core 车道（v1 观测键仅落行；进趋势另开卡）
- ❌ 不改 `--rounds` / `--lane` / record_only 语义，不动字节等价 pin 的口径

## 三、里程碑（2h 卡粒度）

| 卡 | 内容 | 验收 |
|---|---|---|
| **J1 判分工具与接入** | `eval/tools/judge.py` + `run.sh`（judge 块校验、data-gated 三键、染色规则、kill-switch、`SEEK_EVAL_JUDGE_ARGS`） | `scripts/eval-judge-test.sh` 全绿（record 不染色 / gate 翻红 / doubt 两模式不染色 / 无块字节不变 / 畸形 exit 2 / kill-switch / 假端点喂判决）；既有 eval selftests 回归绿 |
| **J2 校准集与报告** | `eval/judge-calibration/fixtures.jsonl` ≥30 行代标 + `judge-calibrate.sh` | 报告出 agreement / 混淆矩阵 / false-pass 单列；代标表呈用户抽查；**达标前无 gate** |
| **J3 首批接线** | `code-review-effort-low`、`read-tail-fact` 接 judge（record 模式）+ README 注记 | 两例行上出现三键、不染色；用例 README 记 rubric 来源 |
| **A1 任务集** | 8 个 `anchor-*` cases + `eval/lane-anchor.txt` | 每题 fixture 自含、check.sh 纯机械、`--lane anchor` 可跑 |
| **A2 锚点自测** | `scripts/eval-anchor-test.sh`（假二进制） | 「解出」绿行 /「不解」红行（failed fact 进 runner_assert_failures）；ws 隔离；teardown 干净 |
| **A3 跨 harness 协议** | `eval/anchor/` + `eval/tools/anchor-crossharness.sh`（opencode CLI，best-effort） | 协议文档含 #2575 边界；脚本无 CLI 时明确降级；产物永不进 results/ |

**顺序**：J1 → J2 → J3（判分层先闭环）→ A1 → A2 → A3。锚点首轮基线（真 key，`--lane anchor`）是发版道人工动作。

## 四、测试计划（按 AGENTS.md failure-mode 清单）

- **取消 / 中途中断**：judge 超时或被 kill → 该轮 verdict=doubt、键照常落行、不染色、不产生半行 JSONL
- **畸形输入**：judge 块非对象 / mode 非法 / rubric 空 → 启动 exit 2 零行写入；判决 JSON 解析失败 → doubt（不许当 pass）
- **环境失败**：kill-switch / `final_text` 空 → 跳过判分，键不出现；judge 调用非零退出 → doubt + reason 记「judge error」
- **并发**：judge 与被测共用结果文件追加路径不变（行级 `>>`），无新并发面
- **持久化往返**：三键可被 jq 直读；`eval-trend.sh` 对新键无感（仍按 `pass` 布尔选行）
- **锚点**：假二进制解出 / 不解两臂；ws 外零写入；`check.sh` 失败路径进 failed fact

## 五、成本

judge 每判一轮 = 被测调用之外 +1 次订阅内单发（judge prompt ≈ rubric + 任务 prompt 摘要 + 2000 字 final_text，flash/pro 档订阅内 $0；计量等价 ~$0.01–0.03/轮）。锚点 8 题 × 1 轮 ≈ 现有单轮全量同量级（订阅内）。跨 harness 对照按需手跑。

## 六、开放问题

1. **gate 模式下 judge 基建故障的口径**：v1 取 fail-open（doubt + 响亮记录，不染红）——与「判分必须可复算」张力最大的一处，J2 报告落地后复议是否引入 fail-closed 选项。
2. **judge 模型轮换**：跨家族菜单（kimi / glm）是否按季度轮换以防 judge 漂移被固化，校准集是否随之重跑。
3. **锚点难度标定**：首轮基线后按区分度调题——同 E2 容差原则，**修题不修阈值**。
4. **校准集刷新节奏**：final_text 分布随版本漂移，fixtures 是否随大版本重抽（重抽 = 重新代标 + 抽查）。
5. E7（「不由 eval 保证」清点）在 v2 仍挂账，与本 PRD 正交，不在此收口。
