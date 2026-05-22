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

**第 1 章：编程智能体是什么**
- 1.1 不是聊天机器人：工具调用循环的本质
- 1.2 逆向分析 Claude Code 的行为
- 1.3 seek 的设计目标与边界
- 1.4 技术选型：Go + DeepSeek，以及为什么

**第 2 章：大语言模型的工具调用协议**
- 2.1 Chat Completions API 的消息结构
- 2.2 Server-Sent Events：流式输出的底层机制
- 2.3 流式工具调用的组装：delta 为什么要按 index 合并
- 2.4 DeepSeek 的差异化能力：前缀缓存、Reasoner、FIM

---

### 第二部分：从零构建 Agent

**第 3 章：M0 — 最小可用 API 客户端**
- 3.1 零外部依赖的 HTTP 客户端设计
- 3.2 实现 SSE 解析器
- 3.3 第一个坑：`reasoning_content` 不能回传给模型
- 3.4 验收：一次完整的流式对话

**第 4 章：M1 — Agent 循环**
- 4.1 消息历史的数据结构
- 4.2 工具调用循环的状态机
- 4.3 三个致命的中断路径：取消、断流、finish_reason 不匹配
- 4.4 孤儿 tool_calls 问题的完整分析与修复

**第 5 章：M2 — 工具系统设计**
- 5.1 工具接口：Name / Description / Schema / Execute
- 5.2 JSON Schema 必须是常量：前缀缓存的代价
- 5.3 Permission Policy：拒绝是工具结果，不是 fatal error
- 5.4 `json.Unmarshal` 静默丢弃未知字段——LLM 拼错字段名时的诊断地狱

**第 6 章：文件工具的细节**
- 6.1 read：行号、分页、目录自动降级
- 6.2 edit：唯一性约束与替换计数验证
- 6.3 grep：先定位再精读，避免整文件塞进 context
- 6.4 write 与 bash：权限管控的两种形态

**第 7 章：M3 — 推理增强**
- 7.1 双模型协作：chat model 调用 reasoner
- 7.2 Think 工具：隔离历史，避免污染下一轮
- 7.3 流式化 Think：StreamDelta 与 push callback 模式
- 7.4 FIM：比 chat 更便宜的"填空"补全

---

### 第三部分：终端用户界面

**第 8 章：M4 — TUI 基础架构**
- 8.1 为什么选 bubbletea
- 8.2 inline 模式 vs alt-screen：一个影响深远的决策
- 8.3 在进入 TUI 前完成终端探针：OSC 11 的陷阱
- 8.4 `WindowSizeMsg` 的不可靠性与合成初始化

**第 9 章：流式渲染与用户交互**
- 9.1 自动滚动：尊重用户的滚动意图
- 9.2 键盘路由：不要把 KeyMsg 发给两个消费者
- 9.3 Per-call 审批：ctx-aware 的双端 channel 设计
- 9.4 品牌 Logo：像素字体、渐变色、逐字母动画——以及 UTF-8 字节 vs 字符坑

---

### 第四部分：持久化与扩展

**第 10 章：M5.1 — 会话持久化**
- 10.1 会话的数据模型：消息、工具调用、元数据
- 10.2 `--resume` / `--continue` / `--list` 的设计权衡
- 10.3 `Repair`：启动时修复损坏会话
- 10.4 `/branch` 与 `/compact`：会话图的分叉与压缩

**第 11 章：M5.2 — Skill 系统**
- 11.1 Skill 的 Markdown frontmatter 格式
- 11.2 多优先级目录扫描（项目 → 用户 → 全局）
- 11.3 按需加载 vs 全量注入 system prompt
- 11.4 `AGENTS.md` 的自动加载：项目级指令的注入

**第 12 章：M5.3 — MCP Client**
- 12.1 MCP 协议概述：工具与资源
- 12.2 JSON-RPC over stdio 的实现
- 12.3 动态工具发现与 Agent 循环的桥接
- 12.4 filesystem MCP server 的端到端验证

---

### 第五部分：走向生产

**第 13 章：M6 — 多 Provider 支持**
- 13.1 `pkg/llm` 的通用接口设计
- 13.2 为什么 `pkg/deepseek` 绝对不能 import `pkg/llm`
- 13.3 Anthropic / OpenAI / Gemini 适配层
- 13.4 Provider 切换时的 TUI 提示与降级策略

**第 14 章：性能与成本优化**
- 14.1 前缀缓存命中率的系统性分析
- 14.2 Token 预算追踪与成本感知定价
- 14.3 FIM 快路径的触发策略
- 14.4 自举测试：用 seek 来开发 seek

**第 15 章：发布**
- 15.1 `go install` 的用户体验
- 15.2 跨平台差异：macOS / Linux / Windows
- 15.3 版本信息的正确姿势：`runtime/debug.ReadBuildInfo`
- 15.4 一键装好的验收标准

---

### 附录

- **附录 A：完整踩坑录** — 来自 `docs/pitfalls.md`，每条都是真实发生的
- **附录 B：DeepSeek API 速查**
- **附录 C：bubbletea 常用模式**
- **附录 D：参考资料与延伸阅读**

---

## 代码仓库

本书对应的代码仓库是 seek。每章的起点和终点对应具体的 git commit，可在各章末尾找到。

```
git clone https://github.com/whyiyhw/seek
```
