# 更新日志（CHANGELOG）

## 已进 main、未部署

- c_tnujb6：resolveProviderName 优先级修复：default_provider 显式声明压过 key 存在性探测（旧行 flag > key 探测 > default_provider，多 key config 里字段永远轮不到、被磁盘上的 key 永久遮蔽）；声明缺 key/未知 provider 启动时报错而非静默换道；--provider 旗标仍最高；guide-config.md 字段说明同步
- c_eha42g：后台记忆管线换车道：dream/distill/memory_observe/skill-distill/-dream 改走后台记忆车道（默认 opendesign 订阅网关 + deepseek-v4.1-flash-eco，env SEEK_MEMORY_BASE_URL/MODEL 与 providers.memory 可覆盖），不再计费自有 DeepSeek per-token key；ACP 车道保持写静默（bot 会话不写 soul）；管线不再依附 dsClient——聊天车道离开 deepseek 后记忆功能继续可用
- c_wjo6wy：R2 verdict 持久化落点：autopilot Report 落盘为可积累 JSONL（§9.5③3）
- c_jxwsfz：--json 空返回的机器可判定信号（judge 前置②：静默空返回 exit 0 陷阱）
- c_lh6tlq：--tools=none 哨兵：零工具面（judge 前置③：安全边界）
- c_kdblc4：R2 无人调度：给 autopilot 挂真触发器（§9.5③1；前置=前两卡合并）
- c_3xyt7w：E3/E4 承重轴 release lane 成员资格定案（E7 待开卡清单#3）
- c_trrsn7：`-p` 车道补 `--effort` / `--temperature` 旗标（judge 前置①：成本 3.2×）
- c_xujwvq：参数校验先于 MCP fork：--tools nosuch 不该先加载 9 个 MCP 工具（judge 前置④）
- c_sozboy：AGENTS.md 会话存储句漂移：legacy .json 迁移已删（65a31d50）+ schema_version 实为 3，文档仍写「migrated on next Save / =2」（eval E7 待开卡 #2，纯文档）
- c_skczdu：autopilot 车道 pref 升 YOLO + flag 解析形状（R2 接线前置，§9.5③2）
- c_yc2thl：ASR 计费链路接线：pricing.ASRCost 至今零调用方，照 TTSCost 接线点对称接上
- c_idinaj：resume 两断点：reconstruct 丢 `[plan: approved] (auto-approve-per-step)` 后缀、persistSession 不回写 Session.Plan
- c_7vpiz5：测试 HOME 泄漏：acp_test/webfrontend_attach_test 未隔离 SEEK_HOME，PDF 落进真实 ~/.seek/projects（坑8 反复停卡疑似根因）
- c_koutux：二线车道开流零重试 + 吞 sc.Err：opencode HTTPS/2 PROTOCOL_ERROR 一次打死整轮（openai client，覆盖 opencode/glm/opendesign/compatible 四车道）
- c_fpnlir：卫生两件：gofmt 4 文件新漂移归位 + 落「多 GOOS 仓 deadcode 单平台误报」pitfall 条目
- c_42a7rf：seek judge 本体·上：internal/judge 纯逻辑内核——单条判读一次 Chat、verdict 三值解析、_meta 聚合（judge §6-5 前半）
- c_dxrjjl：模型×lane 一致性闸 + opendesign 子代理路由：宿主只回传模型名时网关 wire id（eco）落官方 DeepSeek 车道 400——启动闸点名该钉的 lane（resolveProviderName 单源）；modelcap.Serves/LaneOf 分权 + providerOrder 修 map 随机序
- c_4rp2py：--no-save 连带关记忆链：拆 --memory=off|ro|rw 正交开关 + --isolated 一把关（judge 前置缺口②③，§6-6）
- c_3j2ovf：握手预算：车道继承 stdlib 10s TLS 握手超时、超时类失败又与瞬时 reset 共用 500ms 退避，opendesign 一轮死在 `TLS handshake timeout (after 2 attempts)`。改共享 20s transport（克隆 DefaultTransport，proxy/HTTP2 原样）+ 按失败类别退避（`net.Error.Timeout()` → 3s），覆盖 openai/compatible、anthropic、gemini 与 DeepSeek 一线
- c_qjuags：seek judge 本体·下：--max-cost 成本硬顶——pricing 累加、预算尽停派新条、exit 3（judge §6-7）
- c_vmrguu：叙事稿开首稿：seek
- c_tnzudu：路由表与推断口径对齐：opencode/deepseek 条目补全，消掉跨车道报错的死胡同提示（c_dxrjjl 复审发现）
- c_4gw4rd：证据目录归位到 docs/evidence/：两个 .butler-evidence-* 目录 git mv + 全部引用一次性更新
- c_nfeloo：修 ACP/TUI 非 opencode 启动时 modelcap 视觉误判：catalogue grants 只在 provLabel==OpenCode 装
- c_bohzri：doc-slim：docs/pitfalls 超预算 169KB — 按「book-overlap 编辑」既定口径降到 533500B
- c_zib76h：web 审查脚本与资产面小修：截图等侧栏过渡 / 后台夹具路径规范化 / 记忆空态边框 / 统计图「$ $」

## v0.13.1（2026-11-24）

v0.13.1 — 一次只修一条死路的补丁版。二线车道（`pkg/llm` 那侧：Anthropic / OpenAI / Gemini / OpenCode Go）上的空补全不再判死整轮：四条独立根因都收敛到同一句 `_seek_turn_stop internal_error: agent: model returned an empty response`，11-20 起在 `opencode:deepseek-v4.1-flash` 上撞了十几次，每次零产出。同一版把 `max_tokens` 的读法归一：`/effort max` 在二线车道此前完全无效，且我们推导的 base 从不钳模型自己的输出上限。无工具 schema / sysprompt 改动，前缀缓存跨版本照常命中。

### Notable changes

- **二线车道空补全死路（修复）**. 四条根因：①两条 runTurn 的「零事件 + 非 stop finish」分支用 `errors.New` 现场造错误——文本与 `errEmptyResponse` 逐字相同、身份不同，`Prompt` 的 `errors.Is` 自愈闸结构上不可达，整轮直接进 `ErrorEvent`；新增 `errBudgetExhaustedEmpty` + `budgetExhaustedMarker` 补上第三个形状（刻意不复用 `emptyResponseMarker`：后者自称「已重试 1 次」，而这个形状从不重试，复用等于往 transcript 写假账，`--resume` / judge / `--json` 都读它）。②二线车道看不见 reasoning——`pkg/llm` 没有 reasoning 通道，`openai` 的 `streamDelta` 也不声明 `reasoning_content`，思考密集的一轮零 delta 抵达、`started` 恒 false，reasoning-only 自愈分支永远匹配不上。③`runTurnLLM` 从不调 `isSafetyRefusal`，刻意的拒答被当成静默上游判死整轮。④`openai` 装配器按 `0..n-1` 遍历 `accum`、遇 index 空洞就 break，空洞之后的调用全部静默丢弃——改成按键排序。
- **`max_tokens` 单一口径（修复）**. `turnMaxTokens()` 成为两车道唯一权威（此前 `runTurnLLM` 与 `summariseLLM` 读 `New` 里填死的 16K，`/effort max` 的 64K 联动只对 DeepSeek 一线生效）。新增 `internal/budget.OutputLimit(model) (int, bool)`——模型单次输出上限表（编译下限 + 目录覆盖，与上下文窗口同形），`bool` 是承重的：unknown 必须 `ok=false` 而不是 0，否则未知模型上每个请求都成了零输出请求。`opencatalog` 解析第三方目录的 `limit.output` 并下发，刻意绕过 `directLaneIDs` 的费率跳过——单次输出上限是模型属性，跟谁计费无关（同 vision/files 的既有判例）。两处克制：钳制只作用于我们推导出来的 base（显式 `--max-tokens` 原样送：上限表可能过期少报，静默削用户明确给的数字会把「表过期」伪装成「神秘截断」，照发能拿到厂商那句清晰的 `max_tokens too large`）；未知模型完全不碰。
- **eval judge gate 开启（record → gating）**. 2026-11-24 用户拍板：开关就是 per-case `judge.mode` 字段、未另造全局开关，两例 `expect.json` 从 record 转 gate，`SEEK_EVAL_JUDGE=0` 一行关回。两个保留理由照旧留档不清零——单轮余量薄（0.853 贴 0.85 线）、凭证路径非正式（登录透传）。
- **文档**. 判读引擎 PRD 按 2026-11-24 实测基重重排（`--effort`/`--temperature` 提到第 1 位、`ResponseFormat` 从必修降为可选、新增「空返回的机器可判定信号」与「参数校验先于 MCP fork」两项，总计 ~5 天 → ~6-7 天）；判读引擎设计稿与实测基线（50 跑批 + 9 份专项事件流）落账；`feature-inspect-rpc.md` §6 #6/#7 复核签章 + hooks 面板闸口径；叙事稿刷新；四篇新坑（手造空错误的自愈闸不匹配、judge 翻红 ≠ exit 翻红、output 上限钳制、worktree 未跟踪证据目录）。

更早的版本见 [GitHub Releases](https://github.com/whyiyhw/seek/releases)——每个 release body 与其 annotated tag 同文。本文件只保留当前版本，历史版本查 git 历史（`git show vX.Y.Z:CHANGELOG.md`）。
