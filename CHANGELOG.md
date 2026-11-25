# 更新日志（CHANGELOG）

## 已进 main、未部署

- c_wjo6wy：R2 verdict 持久化落点：autopilot Report 落盘为可积累 JSONL（§9.5③3）
- c_jxwsfz：--json 空返回的机器可判定信号（judge 前置②：静默空返回 exit 0 陷阱）
- c_lh6tlq：--tools=none 哨兵：零工具面（judge 前置③：安全边界）

## v0.13.1（2026-11-24）

v0.13.1 — 一次只修一条死路的补丁版。二线车道（`pkg/llm` 那侧：Anthropic / OpenAI / Gemini / OpenCode Go）上的空补全不再判死整轮：四条独立根因都收敛到同一句 `_seek_turn_stop internal_error: agent: model returned an empty response`，11-20 起在 `opencode:deepseek-v4.1-flash` 上撞了十几次，每次零产出。同一版把 `max_tokens` 的读法归一：`/effort max` 在二线车道此前完全无效，且我们推导的 base 从不钳模型自己的输出上限。无工具 schema / sysprompt 改动，前缀缓存跨版本照常命中。

### Notable changes

- **二线车道空补全死路（修复）**. 四条根因：①两条 runTurn 的「零事件 + 非 stop finish」分支用 `errors.New` 现场造错误——文本与 `errEmptyResponse` 逐字相同、身份不同，`Prompt` 的 `errors.Is` 自愈闸结构上不可达，整轮直接进 `ErrorEvent`；新增 `errBudgetExhaustedEmpty` + `budgetExhaustedMarker` 补上第三个形状（刻意不复用 `emptyResponseMarker`：后者自称「已重试 1 次」，而这个形状从不重试，复用等于往 transcript 写假账，`--resume` / judge / `--json` 都读它）。②二线车道看不见 reasoning——`pkg/llm` 没有 reasoning 通道，`openai` 的 `streamDelta` 也不声明 `reasoning_content`，思考密集的一轮零 delta 抵达、`started` 恒 false，reasoning-only 自愈分支永远匹配不上。③`runTurnLLM` 从不调 `isSafetyRefusal`，刻意的拒答被当成静默上游判死整轮。④`openai` 装配器按 `0..n-1` 遍历 `accum`、遇 index 空洞就 break，空洞之后的调用全部静默丢弃——改成按键排序。
- **`max_tokens` 单一口径（修复）**. `turnMaxTokens()` 成为两车道唯一权威（此前 `runTurnLLM` 与 `summariseLLM` 读 `New` 里填死的 16K，`/effort max` 的 64K 联动只对 DeepSeek 一线生效）。新增 `internal/budget.OutputLimit(model) (int, bool)`——模型单次输出上限表（编译下限 + 目录覆盖，与上下文窗口同形），`bool` 是承重的：unknown 必须 `ok=false` 而不是 0，否则未知模型上每个请求都成了零输出请求。`opencatalog` 解析第三方目录的 `limit.output` 并下发，刻意绕过 `directLaneIDs` 的费率跳过——单次输出上限是模型属性，跟谁计费无关（同 vision/files 的既有判例）。两处克制：钳制只作用于我们推导出来的 base（显式 `--max-tokens` 原样送：上限表可能过期少报，静默削用户明确给的数字会把「表过期」伪装成「神秘截断」，照发能拿到厂商那句清晰的 `max_tokens too large`）；未知模型完全不碰。
- **eval judge gate 开启（record → gating）**. 2026-11-24 用户拍板：开关就是 per-case `judge.mode` 字段、未另造全局开关，两例 `expect.json` 从 record 转 gate，`SEEK_EVAL_JUDGE=0` 一行关回。两个保留理由照旧留档不清零——单轮余量薄（0.853 贴 0.85 线）、凭证路径非正式（登录透传）。
- **文档**. 判读引擎 PRD 按 2026-11-24 实测基重重排（`--effort`/`--temperature` 提到第 1 位、`ResponseFormat` 从必修降为可选、新增「空返回的机器可判定信号」与「参数校验先于 MCP fork」两项，总计 ~5 天 → ~6-7 天）；判读引擎设计稿与实测基线（50 跑批 + 9 份专项事件流）落账；`feature-inspect-rpc.md` §6 #6/#7 复核签章 + hooks 面板闸口径；叙事稿刷新；四篇新坑（手造空错误的自愈闸不匹配、judge 翻红 ≠ exit 翻红、output 上限钳制、worktree 未跟踪证据目录）。

更早的版本见 [GitHub Releases](https://github.com/whyiyhw/seek/releases)——每个 release body 与其 annotated tag 同文。本文件只保留当前版本，历史版本查 git 历史（`git show vX.Y.Z:CHANGELOG.md`）。
