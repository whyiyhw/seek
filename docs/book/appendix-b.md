# 附录 B：为什么 seek 是 DeepSeek 原生的——评估驱动的行为调优

seek 的工具描述、工作流指引、提示词策略，**不是从 Claude Code 抄过来的**。它是通过一套"建 case → 跑基线 → 改 → 对比"的评估循环，对 DeepSeek API 一遍遍测出来的。

这不是理念，是实操。附录 A 收录了开发过程中踩过的坑（协议层面 / Go 语言层面）；这份附录收录的是**行为设计层面的决策过程**——每一个"工具 description 应该写什么"、"为什么这句指引放在 description 里而不是 AGENTS.md 里"、"凭什么 10 个词的改动能让模型从 `read→grep→read` 变成 `grep→read`"。

---

## B.1 抄提示词是死路

Claude Code 的 system prompt 可以在网上找到。Anthropic 的 tool descriptions 也是公开的。但把它们的措辞搬进 seek 的工具定义里，**大概率不 work**。

原因不是 DeepSeek "不够好"。是**两个模型的注意力分布不同**：

- Claude 模型在长 system prompt 上的检索能力极强——一段 5000 token 的指示，它能精准提取相关段落。
- DeepSeek 的前缀缓存机制让 prompt 的**物理位置**直接决定注意力权重：离用户消息越近的 token，越容易被模型注意到。

所以 Claude Code 可以把大量行为指引塞进 system prompt 的各个角落；但 seek 如果把同样的东西放进 AGENTS.md（seek 等价于 system prompt），DeepSeek V4-Flash 大概率会忽略它们——AGENTS.md 在 token 序列的最前端，离实际的 `tool_call` 决策隔着几千个 token。

**抄 prompt 解决不了这个问题。你得用 DeepSeek 测 DeepSeek。**

---

## B.2 `eval/` 框架的设计哲学

`eval/` 不是单元测试。它不 mock DeepSeek API——**它就是真的调 DeepSeek API**。每一次跑 case，都是真金白银的 API 调用（$0.02–0.05/case），返回的 JSONL 记录里包含精确的 tool_call 序列、token 消耗、耗时。

框架的设计约束：

1. **每个 case 测一个具体行为**——不是"模型聪明吗"，而是"模型用 `git show` 读文件吗"、"模型不 read 就 edit 吗"。
2. **expect.json 用边界而非绝对值**——`max_git_calls: 0`、`min_grep_calls: 1`。因为 DeepSeek 的随机性意味着两次完全相同的 run 可能工具调用次数不同（grep 一次 vs 两次），但方向是一致的（不该用 git）。
3. **结果写进 git**——`eval/results/` 是 traceable 的。你可以 `git log eval/results/` 观察某个行为是哪个 commit 引入、哪个 commit 修复的。
4. **case 先于改动**——在改任何 tool description 之前，先建 case、先跑 baseline。不做"我感觉改了有用"的改动。

---

## B.3 为什么 tool description 是最有效的干预点

这是通过 `eval/cases/tool-selection/` 实测出来的结论，不是预判。

**实验设计**：

- 写一个 prompt，要求模型读三个 Go 文件的 `const description` 并比较字数。
- 这个任务纯只读，最优路径是 `grep` 定位行号 → `read(offset=N)` 精确读。
- 做两次 run：改 description 前（baseline）和改之后。

**结果**：

> 注：下表记录 2026-05 的行为快照——当时 `read` 默认输出 50 行。现在的 `read` 已改为：≤32 KiB 的小文件整体返回、`limit` 默认 200、header 报告 EOF 行号（见 `docs/test-plan-read-tool.md`）。

| | Baseline | After |
|---|---|---|
| 工具顺序 | `read(50行) → grep → read(offset)` | **`grep → read(offset)`** |
| 消耗 | 读 3 文件 × 50 行 = ~150 行无意义输出 | 只读 3 行 |
| git_calls | 0 | 0 |
| bash_calls | 0 | 0 |

改前三行 `const description`，每行加 10–20 词。没有改任何逻辑代码。但模型的行为从一个"先逛再找"的模式，切换到了"先找再走"。

**为什么有效**：

Tool description 是跟着 JSON schema 一起发给 API 的。JSON schema 在消息序列里紧挨着助理的生成位置——模型要构造 `tool_call`，必须先读完 schema。这意味着 description 的每个 token 都在模型的 **active attention window** 里。

而 AGENTS.md 在对话的最前端——当模型生到第 10 个 turn 时，AGENTS.md 的内容已经被几千 tokens 的对话历史推到注意力边缘了。同样的指引，放在 tool description 里比放在 AGENTS.md 里，被模型采纳的概率高一个数量级。

这不是猜测。`eval/cases/tool-selection/` 的 baseline 就是 AGENTS.md 已经包含了 "grep→read" 指引时的行为——模型依然先 read 再 grep。改了 tool description 之后，行为才矫正过来。

---

## B.4 评估驱动的迭代节奏

seek 的工具行为不是一次性设计完的。它是一轮一轮测出来的：

| 阶段 | 做的事 | 耗时 |
|---|---|---|
| 发现 | 从真实 session 中观察到一个不当行为（`git show` 读文件、不 read 就 edit、`bash ls`） | 日常使用 |
| 建 case | `mkdir eval/cases/<name>` → 写 `prompt.txt` + `expect.json` | 10 分钟 |
| baseline | 跑一次，记录当前行为 | 30 秒 |
| 调 | 改 tool description / AGENTS.md / workflow 指引 | 5 分钟 |
| 验证 | 再跑一次，对比工具调用序列 | 30 秒 |
| 锁入 | 结果写进 `eval/results/`，commit | 1 分钟 |

一次完整迭代 < 20 分钟，成本 < $0.10。但每一次迭代产出的是一条 **有数据支撑的行为改进**，不是"我觉得 prompt 应该这样写"的玄学。

---

## B.5 这和抄 Claude Code 的区别

如果 seek 是抄 Claude Code 的提示词：

- 它的 tool description 会是 Anthropic 的措辞，针对 Claude 的注意力模式优化
- 它的行为指引会大量放在 system prompt 里（因为 Claude 的长上下文检索能力强）
- 它不会有一个评估框架——"抄过来就行了，要测什么？"
- 用 DeepSeek 跑的时候，行为会退化——因为两个模型读 prompt 的方式不一样

**seek 选择的路**：

- 每一个行为决策都是在对 DeepSeek API 的实测中形成的
- tool description 是主战场，AGENTS.md 是辅助——这是 DeepSeek 前缀缓存机制决定的
- `eval/` 不是可选品，是**开发工具链的一部分**——和 `go test` 同等地位
- 结果就是：seek 在 DeepSeek 上跑的行为，不是"Claude Code 的劣化版"，而是**专门为 DeepSeek 调出来的原生行为**

---

## B.6 一个具体对比

以 "read before edit" 这个规则为例：

| | Claude Code 的做法 | seek 的做法 |
|---|---|---|
| 位置 | system prompt（长文本中的一句话） | `edit` 工具的 `const description` |
| 模型看到的方式 | 在对话最前端，可能被注意力衰减 | 在每次可能调用 edit 时，紧挨着 tool_call 生成位置 |
| 有效性验证 | Anthropic 内部测试 | `eval/cases/tool-selection/` 的 baseline → after 对比 |
| 可以跨模型移植吗 | 换成 DeepSeek → 行为退化 | 本身就是为 DeepSeek 调的 |

同样的规则，同样的语义。但因为**放的位置不同**，行为效果差一个量级。这个差异不是读文档能知道的——它是跑 eval 跑出来的。

---

## B.7 这套方法论的适用范围

评估驱动的行为调优不只是 tool description 的事。同样的循环可以用在：

- **Skill 注入时机**：skill body 是 startup 时全量注入，还是按需 `Skill()` 调用后注入？哪种 token 效率更高？
- **plan-mode 提示词**：`propose` 的 description 写多详细？太短模型乱提，太长模型不看。
- **bash 的安全边界**：`readonly.go` 的白名单应该多宽？加一个命令 → 跑 `bash-overreach` case → 看误杀率。
- **跨模型行为**：换 Anthropic / OpenAI provider 时，同一组 case 是否仍然 PASS？行为是否退化？

**原则不变**：建 case → 跑 baseline → 改 → 对比。用数据代替直觉。

---

## B.8 总结

seek 的"DeepSeek 原生"不是一个 marketing 说法。它是**用 DeepSeek 的 API 一 case 一 case 测出来的行为调优结果**。

- 不是抄来的提示词适配版
- 不是"理论上应该 work"的设计
- 是每一次 `PASS` 和 `FAIL` 背后的真实 tool_call 序列决定的

如果你要改进 seek 的行为：**先建 eval case，再改代码**。直觉会骗人，API 返回的 JSONL 不会。
