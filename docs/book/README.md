# 从零实现 Claude Code

> 构建你自己的 AI 编程智能体

---

## 关于本书

本书记录了 **seek** 的完整构建过程——一个从零开始、用 Go 实现的 DeepSeek 驱动编程智能体。

它不是一本"调用 AI API 做应用"的入门书。它要回答的问题是：Claude Code 这样的工具，底层是怎么运转的？如果你自己来造一个，每一层的设计决策是什么，会踩哪些坑，代价是什么？

全书以真实的 commit 历史为骨架，每个里程碑对应一组章节。你可以跟着做——每章结束时都有一个可以运行的版本。

**目标读者**：有 Go 基础（或愿意边看边学），对 LLM 工具调用、终端 UI、流式协议感兴趣的开发者。

---

## 目录

### 前言

- [前言：为什么要自己造一个](preface.md)

---

### 第一部分：理解编程智能体

**[第 1 章：编程智能体是什么](chapter-01.md)**
- 1.1 不是聊天机器人：工具调用循环的本质
- 1.2 逆向分析 Claude Code 的行为
- 1.3 seek 的设计目标与边界
- 1.4 技术选型：Go + DeepSeek

**[第 2 章：大语言模型的工具调用协议](chapter-02.md)**
- 2.1 Chat Completions API 的消息结构
- 2.2 Server-Sent Events：流式输出的底层机制
- 2.3 流式工具调用的组装：delta 为什么要按 index 合并
- 2.4 DeepSeek 的差异化能力（前缀缓存 / Reasoner / FIM）

---

### 第二部分：从零构建 Agent

**[第 3 章：M0 — 最小可用 API 客户端](chapter-03.md)**
- 3.1 零外部依赖的设计决策
- 3.2 Client 的结构
- 3.3 类型系统：把 API 文档翻译成 Go
- 3.4 实现 SSE 解析器
- 3.5 测试策略：用 httptest 模拟 DeepSeek
- 3.6 验收：一次完整的流式对话

**[第 4 章：M1 — Agent 循环](chapter-04.md)**
- 4.1 消息历史的数据结构
- 4.2 Agent 循环的状态机
- 4.3 工具调用 delta 的组装
- 4.4 孤儿 tool_calls：一个让会话损坏的 bug
- 4.5 事件系统的设计

**[第 5 章：M2 — 工具系统设计](chapter-05.md)**
- 5.1 工具接口：三个方法，一个约定
- 5.2 JSON Schema 必须是常量
- 5.3 工具注册表
- 5.4 Permission Policy：拒绝是工具结果，不是崩溃
- 5.5 参数解析：让模型的拼写错误有意义
- 5.6 read 工具：简单背后的一个设计决策
- 5.7 grep 工具：找位置，再精读

**[第 6 章：M3 — 推理增强](chapter-06.md)**
- 6.1 为什么要"工具化"reasoner（含 V4 时间线脚注）
- 6.2 think 工具的关键设计：完全隔离历史
- 6.3 流式化 think：当工具也有"输出过程"
- 6.4 截断：防止推理结果撑爆 context
- 6.5 FIM：填空补全，不是全文重写

---

### 第三部分：终端用户界面

**[第 7 章：M4 — 终端用户界面](chapter-07.md)**
- 7.1 为什么选 bubbletea
- 7.2 inline 模式 vs alt-screen：一个影响深远的决策
- 7.3 OSC 11 探针：在进入 TUI 前完成终端检测
- 7.4 WindowSizeMsg 的不可靠性
- 7.5 Elm 架构在 seek TUI 里的应用
- 7.6 键盘路由：不要把 KeyMsg 发给两个消费者
- 7.7 自动滚动：尊重用户的滚动意图
- 7.8 品牌 Logo：像素字体、渐变色、逐字母动画

**[第 8 章：M4.5 — TUI 稳定化](chapter-08.md)**
- 8.1 一个稳定化里程碑的形状
- 8.2 Esc 中断的三层闭环
- 8.3 per-call 审批：policy 三态 + inline y/N/a
- 8.4 slash 命令补全菜单
- 8.5 `@` 路径补全
- 8.6 prompt 历史：上下方向键翻
- 8.7 状态栏：streaming 计时 + token 估算
- 8.8 mid-stream 输入：queue 与 steer

---

### 第四部分：持久化与扩展

**[第 9 章：M5.1 — 会话持久化](chapter-09.md)**
- 9.1 我们到底要持久化什么
- 9.2 为什么是 JSONL 而不是单文件 JSON
- 9.3 Save：原子写 + 一次性重写
- 9.4 Load 与透明的 schema 迁移
- 9.5 loadMeta：只读 line 1 让 `--list` 不再卡顿
- 9.6 Repair：把孤儿 tool_calls 在加载时清掉
- 9.7 `--resume` / `--continue` / `--list` / `--no-save`
- 9.8 一个测试观察：`loadMeta` 不能用 `Load`

**[第 10 章：M5.2 — `/branch` 与 `/compact`](chapter-10.md)**
- 10.1 数据模型：一个 ParentID 字段，承载整棵树
- 10.2 Fork：深拷贝的边界
- 10.3 `/branch`：用户层面 + 实现细节
- 10.4 `/compact`：保留完整历史的 fork-snapshot 设计
- 10.5 摘要的 prompt 设计
- 10.6 user + assistant 双消息引导
- 10.7 一个细节：tracker.Record 在 compact 路径上
- 10.8 链可视化（半步）

**[第 11 章：M5.3 — Skill 系统与 `AGENTS.md` 自动加载](chapter-11.md)**
- 11.1 Skill 解决的问题
- 11.2 文件格式：Markdown + 简化的 frontmatter
- 11.3 4+1 层优先级扫描
- 11.4 按需注入：manifest vs body
- 11.5 内置 skill：`go:embed` 保持单二进制
- 11.6 `AGENTS.md` 自动加载
- 11.7 一个观察：Skill 和 AGENTS.md 的"按需 vs 强制"

**[第 12 章：M5.4 — MCP client](chapter-12.md)**
- 12.1 MCP 是一个"瘦协议 + 子进程模型"
- 12.2 JSON-RPC 2.0：一个很瘦的协议
- 12.3 子进程 spawn：`exec.CommandContext` + pipe
- 12.4 三步握手：initialize → tools/list → 准备就绪
- 12.5 Bridge：把 MCP 工具假装成 seek 工具
- 12.6 名称冲突：动态前缀
- 12.7 `mcp.json`：兼容 Claude Code / Cursor 的格式
- 12.8 集成：启动时一次加载、错误不致命
- 12.9 一个端到端验证

---

### 第五部分：走向生产

**[第 13 章：成本与上下文预算](chapter-13.md)**
- 13.1 `cache.Tracker`：每轮一条记录, 成本在 Record 时锁定
- 13.2 cumulative vs last-turn：埋了三章的反直觉
- 13.3 `pricing`：定价表 + 离峰窗口 + V4 别名 sunset
- 13.4 `budget`：把 token 数翻译成"几分严重"
- 13.5 `finish_reason="length"`：一条没人提就静默的错误
- 13.6 三个模块怎么 wire 到一起
- 13.7 一个观察：成本意识是项目里的"诚实度指标"

**[第 14 章：M6 — 多 Provider 支持](chapter-14.md)**
- 14.1 设计前提：`pkg/deepseek` 是第一公民，不参与抽象
- 14.2 `pkg/llm` 接口：故意瘦的中间件
- 14.3 `translate.go`：第一公民 ↔ 第二公民的边界
- 14.4 Anthropic：tool_result 必须合并
- 14.5 Gemini：没有 call ID + `systemInstruction` 单独字段
- 14.6 OpenAI：必须显式打开 streaming usage
- 14.7 `compatible`：vLLM / Ollama / SiliconFlow 的薄包装
- 14.8 Provider 选择：标志 + env 自动探测
- 14.9 TUI Provider banner：能力降级的可视化
- 14.10 一个测试观察：每个 provider 都有 6-7 个 buildRequest 测试

**[第 15 章：M7 与 v1.0 发布](chapter-15.md)**
- 15.1 JSON 输出模式：让脚本能用 seek
- 15.2 自举：用 seek 写 seek
- 15.3 `go install`：单二进制发布的最小工作姿势
- 15.4 `runtime/debug.ReadBuildInfo`：版本号的正确姿势
- 15.5 跨平台：三条主要差异
- 15.6 M7 polish：浮动 help、`/new`、`--theme`
- 15.7 v1.0 验收：每条都对得上
- 15.8 v1.0 之后:这本书没停, 只是换了个版本号

---

### 第六部分：v1.0 之后（第 16–18 章）

**[第 16 章：M5 — 三层认知记忆 (L/M/S)](chapter-16.md)** *(v0.2.x, PRD v1)*
- 16.1 为什么需要"记忆", 不只是"会话历史"
- 16.2 数据模型: 三层用三种不同的形态
- 16.3 注入机制: PrePromptHook 与 prefix-cache 的死结
  - 16.3.1 首版:每轮重注入 → 写一条 M 就让整个前缀 cache miss
  - 16.3.2 M5.9 快照+delta: Turn 1 注入, 后续不变, 变化在尾部追加
  - 16.3.3 字节稳定性: 确定性排序 + 时间无关截断键
- 16.4 调用面:四个工具 + 两个命令 + 一组自动化
  - 16.4.1 memory_recall / memory_remember / memory_observe / memory_amend / memory_archive
  - 16.4.2 `/distill` TUI review + `seek -dream` CLI
  - 16.4.3 M 不是 bug tracker: 一个工具描述引发的设计修正
- 16.5 AutoSourced 的生命周期 (M5.11)
  - 16.5.1 从布尔到连续: observe_count 累进置信度
  - 16.5.2 recall 驱动自升格: 模型"用出来"的记忆比"看到"的更可信
  - 16.5.3 `[auto]` 前缀: 让模型在 index 层面区分可信/未确认
- 16.6 M5.13 observe 反馈闭环
  - 16.6.1 空返回的设计理由 vs 模型盲区
  - 16.6.2 聚合 stats 注入 M-index: 16 行代码的权衡
- 16.7 GC: 衰减打分 + 双悬崖归档 + 灰度保护
- 16.8 L 层维护 (M5.10)
  - 16.8.1 dream:从 M 到 L 的跨项目蒸馏
  - 16.8.2 evaluatePending: 三条机械规则 (升格/删除/保留)
  - 16.8.3 为什么 dream 用 V4-Pro (低频+高杠杆)
  - 16.8.4 auto-dream 默认开启 + ≥2 项目前置检查
- 16.9 项目身份: 用 `abs path → sha256[:16]` 哈希作目录名
- 16.10 已知局限 (M5.14–M5.15, v3)
  - 16.10.1 单项目用户的 L 层透明
  - 16.10.2 M-index 在 1500 token 截断线下的取舍
  - 16.10.3 精确 name 匹配 vs 语义检索
- 16.11 一个观察: 记忆是 seek 从"工具"到"伙伴"的换挡

**[第 17 章：M8 — Skill 生命周期管理](chapter-17.md)** *(v0.3.x, PRD v2)*
- 17.1 v0 Skill 的三个缺口
- 17.2 三个并行的变化
- 17.3 目录包: 与 Anthropic Agent Skills 规范对齐
- 17.4 安装来源:local / git / https 三条路径
- 17.5 `.install.json` sidecar:为什么不和 SKILL.md frontmatter 合并
- 17.6 `seek skill update`:三种来源各自的"刷新"语义
- 17.7 调用统计: `.stats.jsonl` 与 PIPE_BUF 单写原子性
- 17.8 CLI / TUI 子命令族
- 17.9 模型驱动的 skill 安装: `skill_fetch` / `skill_commit` 工具
- 17.10 一个观察:可观测性是 v0 到 v1 的换挡

**[第 18 章：Plan Mode 与交互工具](chapter-18.md)** *(v0.3.x–v0.4.x)*
- 18.1 从"单向 toggle"到"分析→提案→执行→报告"闭环
- 18.2 分岔点: 为什么 plan-mode 需要自解析, 而不是靠工具返回值
- 18.3 `propose` 工具:problem + steps + TUI 选择器
  - 18.3.1 在 plan-execute 子态下禁止 filesystem 和 shell 写入
  - 18.3.2 adjust: 用户说"改"时不丢掉已完成的工作
- 18.4 `git` 工具: 只读 git wrapper + plan-mode 豁免
  - 18.4.1 为什么不用 bash git: 安全 + plan-mode 可执行
  - 18.4.2 覆盖本地子命令与网络只读 (`ls-remote`)
- 18.5 `ask_user` 工具: 内联 TUI 选择器
  - 18.5.1 三个适用条件 vs 与 permission 提示的职责边界
  - 18.5.2 当用户取消: 模型的最佳猜测 + 明确声明
- 18.6 plan-mode 内置 skill: 提示自动注入
- 18.7 `/review` 与 per-message 模式提醒
- 18.8 一个观察: 确认门改变了 agent 和用户之间的权力结构

**[第 19 章：M9.0–M9.1 — 双层 Checkpoint 安全网](chapter-19.md)** *(v0.4.0)*
- 19.1 问题: 不可逆的破坏性操作让 `--yolo` 心理成本很高
- 19.2 两层互补设计: git ref 快照 (粗粒度) + 文件 CAS undo/redo (细粒度)
- 19.3 Git checkpoint: `git stash create` + `refs/seek/checkpoints/` 命名空间
- 19.4 文件 checkpoint: content-addressed blob + index.jsonl + 外部修改检测
- 19.5 CLI / TUI 命令: `seek checkpoint {list,restore,prune}` · `seek undo/redo`
- 19.6 TUI 斜杠命令: `/checkpoints` · `/restore` · `/undo` · `/redo`
- 19.7 验收标准 12/12 全绿

**[第 20 章：M9.2–M9.3 — Shell Hooks 可扩展性](chapter-20.md)** *(v0.4.1)*
- 20.1 动机: 把 `internal/hooks` Registry 开放给用户
- 20.2 设计约束: stdout 不进 prompt · 只 deny/observe · 信任前置
- 20.3 hooks.toml: 用户级 + 项目级叠加，VS Code 风格 trust 询问
- 20.4 ShellRunner: pre_tool deny + 五个 observer
- 20.5 安全机制: `bash -n` 静态检查 · timeout · 并发 audit log
- 20.6 CLI / TUI 命令: `seek hooks {list,check,trust,audit}`
- 20.7 验收标准 12/12 全绿

---

### 附录

- **[附录 A：踩坑录索引](appendix-a.md)** — 书中 #1–#16 编号坑 ↔ [`docs/pitfalls.md`](../pitfalls.md) 条目对照表；附"书中提过但未编号"的延伸阅读列表
- **[附录 B：为什么 seek 是 DeepSeek 原生的](appendix-b.md)** — 评估驱动的行为调优方法论；为什么 tool description 是最有效的干预点；和抄 Claude Code 提示词的本质区别

> 原计划中的"DeepSeek API 速查"、"bubbletea 常用模式"、"参考资料"三份附录暂未撰写。第 2、6、13 章已覆盖 DeepSeek API 的关键细节；第 7、8 章覆盖 bubbletea 的实用模式。如未来有读者需求再单独成册。

---

## 代码仓库

本书对应的代码仓库是 seek。每章的起点和终点对应具体的 git commit，可在各章末尾找到。

```
git clone https://github.com/whyiyhw/seek
```
