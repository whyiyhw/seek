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
- 13.1 `cache.Tracker`：什么都不做的核心
- 13.2 cumulative vs last-turn：埋了三章的反直觉
- 13.3 `pricing`：定价表 + 离峰窗口
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

**[第 15 章：M7 与发布](chapter-15.md)**
- 15.1 JSON 输出模式：让脚本能用 seek
- 15.2 自举：用 seek 写 seek
- 15.3 `go install`：单二进制发布的最小工作姿势
- 15.4 `runtime/debug.ReadBuildInfo`：版本号的正确姿势
- 15.5 跨平台：三条主要差异
- 15.6 v1.0 验收：每条都对得上
- 15.7 这本书是怎么停的

---

### 附录

- **附录 A：完整踩坑录** — 见 [`docs/pitfalls.md`](../pitfalls.md)，每条都是真实发生的。书中 16 条编号坑（#1–#16）均与此处条目对应

> 原计划中的"DeepSeek API 速查"、"bubbletea 常用模式"、"参考资料"三份附录暂未撰写。第 2、6、13 章已覆盖 DeepSeek API 的关键细节；第 7、8 章覆盖 bubbletea 的实用模式。如未来有读者需求再单独成册。

---

## 代码仓库

本书对应的代码仓库是 seek。每章的起点和终点对应具体的 git commit，可在各章末尾找到。

```
git clone https://github.com/whyiyhw/seek
```
