# seek 产品需求文档（PRD）

本目录只放**规划中 / 在飞**的 PRD 与长期锚点。已交付且只剩历史价值的文档在 [`archive/`](archive/)（v8 D5 归档减法，2026-11-17）。

## 版本一览（活跃）

| 文件 | 对应版本 | 状态 | 说明 |
|------|---------|------|------|
| [`v7.md`](v7.md) | seek v0.8.x | 🚀 已交付 | 打擂台 Reasonix 专属护城河（Autopilot / 沙箱 / ACP / 离线 OCR 四柱落地）。留作 v8 的前置阅读。|
| [`v8.md`](v8.md) | seek v0.12.x 起 | 📐 规划稿 | 薄内核 + 外围插件 + 可度量的自由生长。六柱：R 证据与裁判 / S 摩擦观测 / T 裸核对照与例行消融 / U 薄内核与外围接缝 / V 一次性进程与端 / W 试验田。第一波（R1/S1/T1/T2/U1）已开卡；§6 五项已拍板（2026-11-17）。|
| [`vision.md`](vision.md) | 长期（3–5 年） | ⭐ 北向星 | seek 的未来愿景：从编程助手演化为本地计算机智能终端。方向而非路线图。|

## 独立 feature PRD（活跃）

| 文件 | 状态 | 说明 |
|------|------|------|
| [`feature-orchestration.md`](feature-orchestration.md) | 📐 设计稿（P1 路由已交付） | 编排承重轴：后台子代理生命周期 + 模型路由（P1 已随 v0.11 交付）+ P2 类型化 agent + P3 hooks 面。|
| [`feature-hooks-injection.md`](feature-hooks-injection.md) | 📐 设计稿 | hooks 注入面（编排 P3-a）：缓存稳定区与受控注入区分离。v8 D3 拍板延后到第二波之后。|
| [`feature-memory-skill-bridge.md`](feature-memory-skill-bridge.md) | 📐 设计稿 | memory↔skill 打通：蒸馏落点 + 生命周期对齐（c_5fdsqb 设计已进 main，里程碑见其 §9）。|
| [`feature-deepseek-app-api.md`](feature-deepseek-app-api.md) | 🚀 ②③④ 已交付（v0.12.0）· ① 判红关闭 | DeepSeek 应用 API 四项评估：②文件输入（F1–F5）/ ③ASR+TTS 客户端 + 语音 frontend（V1a 播报无 CLI 开关，V2 按住说话已通）/ ④`safety_check` 全落地；①`read_url` 因与 function tools 共存性实测为红不接。v8 D3：归档待办。|
| [`feature-remote-control-plane.md`](feature-remote-control-plane.md) | 🚀 已交付（无剩余排期） | Frontend 端口定义所在——`seek serve` sidecar + 会话绑定路由 + 直连执行。v8「新的端只经 Frontend 端口或 ACP」的参照物，故留活跃区。|
| [`feature-web-frontend.md`](feature-web-frontend.md) | 📐 设计稿（MVP 已交付） | Web Frontend：MVP 聊天已交付；P2 任务面板 → P3 资产面设计中。|
| [`feature-slim-loop.md`](feature-slim-loop.md) | 📐 设计稿 | 减法机制 L2 账与刀 / L3 自噬循环；本目录的预算由它看管。|
| [`feature-eval-v2.md`](feature-eval-v2.md) | 📐 设计稿（2026-11-19） | eval 判定口径重做：单次抽样 → `--rounds` + `min_pass_rate`；补 plan / 后台子代理 / serve-web 三条承重轴；发版 release lane + 夜间 core 入口；能确定化的检查移回 Go 单测。|
| [`feature-mcp-client.md`](feature-mcp-client.md) | 📐 深度集成设计完成（可开卡） | MCP 客户端 infra 已于 M5.4 交付。深度集成设计（2026-11-16）以 Semble 为第一验证目标：引导三层落点（工具 schema / sysprompt MCPSection / AGENTS.md）+ 路由决策（默认 `server__tool` 前缀、grep 结果内 hint 后手、否决调度层拦截）+ 三臂对照评估口径，拆 D1–D5 五张 2h 卡。|
| [`feature-skill-distill.md`](feature-skill-distill.md) | 🚀 一~三期已交付 | Skill 自动蒸馏（S→K）：四层新颖性漏斗 + `.drafts/` 草稿区 + 人审 install 铁律。AGENTS.md 架构节引用。|
| [`feature-plan-mode.md`](feature-plan-mode.md) | 🚀 已交付 | Plan 模式 v2：analyze → propose → execute 闭环 + event-sourcing resume。AGENTS.md 权限节引用。|
| [`feature-permission-refactor.md`](feature-permission-refactor.md) | 🚀 已交付 | Permission 两轴（Preference/Workflow）重构。AGENTS.md 权限节引用。|
| [`feature-inspect-rpc.md`](feature-inspect-rpc.md) | 🗄 已归档（冻结面留此） | RPC 单一数据面权威 + `--rpc` stdio 三 method 冻结的决定记录。AGENTS.md RPC 节引用。|
| [`feature-webfetch.md`](feature-webfetch.md) | 🚀 已交付 | WebFetch SSRF 防御外部读取。AGENTS.md 架构节引用。|
| [`feature-active-memory.md`](feature-active-memory.md) | 🚀 已交付 | 主动记忆整理。AGENTS.md 架构节引用。|
| [`feature-i18n.md`](feature-i18n.md) | 🚀 已交付 | i18n view 层目录 + 奇偶校验。AGENTS.md 架构节引用。|

## 归档区 [`archive/`](archive/)

已交付 / 已决策、只剩历史价值的文档（git mv 保留全文史，一行一条）：

- [`v0.md`](archive/v0.md) — 初始版本 M0–M8：核心设计决策、架构分层、里程碑记录
- [`v1.md`](archive/v1.md) — v0.2.x 三层记忆 L/M/S + 自动化层
- [`v2.md`](archive/v2.md) — v0.3.x Skill 生命周期（目录包 / install / 调用统计）
- [`v3.md`](archive/v3.md) — v0.4.x 三柱总览（可逆 / 可扩展 / 可定制）
- [`v4.md`](archive/v4.md) — v0.5.x 闭环工效（suggested-reply 自校准）
- [`v5.md`](archive/v5.md) — v0.6.x 代理编排 umbrella（子代理+worktree / cron+routines）
- [`v6.md`](archive/v6.md) — v0.7.x 单点工具补齐 P1 closure
- [`feature-checkpoint.md`](archive/feature-checkpoint.md) — v0.4.0 柱 A：双层 checkpoint（git ref 快照 + 文件 CAS undo/redo）
- [`feature-shell-hooks.md`](archive/feature-shell-hooks.md) — v0.4.1 柱 B：用户 shell hooks（deny/observe）
- [`feature-tui-ergonomics.md`](archive/feature-tui-ergonomics.md) — v0.4.2 柱 C：斜杠补全 + 自定义 keybindings
- [`feature-suggested-reply.md`](archive/feature-suggested-reply.md) — v0.5.0 柱 D：next-turn 预测 + mispredict 反向注入
- [`feature-subagent.md`](archive/feature-subagent.md) — v0.6.0 柱 G：子代理 + worktree 隔离
- [`feature-routines.md`](archive/feature-routines.md) — v0.6.1 柱 H：cron / wakeup / push 时序触发（零 daemon）
- [`feature-askuser-v2.md`](archive/feature-askuser-v2.md) — v0.7.0 柱 I：AskUserQuestion v2 多题 + preview
- [`feature-code-review.md`](archive/feature-code-review.md) — v0.7.0 柱 J：code-review skill + `/code-review` 命令
- [`feature-bash-monitor.md`](archive/feature-bash-monitor.md) — v0.7.0 柱 K：后台 bash + monitor 工具
- [`feature-lsp.md`](archive/feature-lsp.md) — v0.7.0 柱 L：LSP references 语义找引用
- [`feature-mobile-push.md`](archive/feature-mobile-push.md) — v0.7.0 柱 M：移动端 webhook 桥
- [`feature-autopilot.md`](archive/feature-autopilot.md) — v0.8.0 柱 N：autopilot 无人值守编排
- [`feature-sandbox.md`](archive/feature-sandbox.md) — v0.8.0 柱 O：OS 沙箱（seatbelt / landlock）
- [`feature-acp.md`](archive/feature-acp.md) — v0.8.0 柱 P：ACP 编辑器集成
- [`feature-vision.md`](archive/feature-vision.md) — v0.9.x：原生视觉输入（取代柱 Q OCR）
- [`feature-image-paste.md`](archive/feature-image-paste.md) — v0.8.x：剪贴板图片采集（出口已改喂原生视觉）
- [`feature-goal.md`](archive/feature-goal.md) — v0.8.x：`/goal` 单 agent 死磕达标自治循环
- [`feature-paseo-provider.md`](archive/feature-paseo-provider.md) — paseo 作为 acp 型 provider（butler drive 车道，缺口已随 v0.11 补齐）
- [`feature-edit-read-before.md`](archive/feature-edit-read-before.md) — 决策记录（c_hkqvwk）：维持软提示，不接结构性方案
- [`notes-event-sourcing-adoption.md`](archive/notes-event-sourcing-adoption.md) — 事件溯源 fold 适用性评估（dsh 借鉴线）
- [`notes-memory-skill-gap.md`](archive/notes-memory-skill-gap.md) — memory↔skill 断层盘点（自进化内环备料）
- [`notes-v5-closeout.md`](archive/notes-v5-closeout.md) — v5 收伞三分拍板（不续期）
- [`feature-image-ocr.md`](archive/feature-image-ocr.md) — 柱 Q 本体（被 feature-vision 取代，代码已移除）
- [`feature-plan-tasklist.md`](archive/feature-plan-tasklist.md) — 被 feature-plan-mode 取代

## 阅读指引

- **第一次了解 seek 架构**：先读 [`archive/v0.md`](archive/v0.md) §1–4（设计决策比交付记录更重要）
- **参与当前开发**：读 [`v8.md`](v8.md)（规划稿）或查看最近 commit 了解最新交付
- **想知道 Memory 怎么工作**：读 [`archive/v1.md`](archive/v1.md) —— 已交付 reference
- **追 bug / 理解某个包为什么这样设计**：回归档区查对应章节 + `docs/book/` 系统设计书

## 版本演进规则

1. 每个大版本一个独立 `.md` 文件，不追加已有文件。
2. 版本之间是**叠加关系**——v1 依赖 v0 的架构基础，除非显式说明"此决策在 v1 中已变更"。
3. 一个版本交付后，其 PRD 移入归档区（git mv，信息不丢），不再修改（勘误除外）。

## 未来愿景

[`vision.md`](vision.md) 描述了 seek 的长期北向星——从编程助手演化为**本地计算机智能终端**。它不是某个版本的路线图，而是指导远期决策的方向框架。愿景文档不随版本迭代更新，只随认知跃迁而修订。
