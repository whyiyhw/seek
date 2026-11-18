# seek Hooks — 生命周期拦截 / Lifecycle interception

Hooks 是 seek 的**生命周期拦截点**，让你在 agent 的关键事件（如 Prompt 前、Tool 使用前后、会话开始/结束）插入自定义逻辑。memory 子系统就是通过 hooks 实现的。

> Hooks define lifecycle interception points for the seek agent — inject custom logic before/after prompts, before/after tool use, and at session boundaries.

设计决策见 [`docs/prd/archive/feature-shell-hooks.md`](prd/archive/feature-shell-hooks.md)（柱 B）与 [`docs/prd/feature-hooks-injection.md`](prd/feature-hooks-injection.md)（注入三区模型，M-O3.2 落地）。

---

## 1. 注入三区模型 / The three-zone injection model

seek 的成本护城河是 DeepSeek 的**前缀缓存**：缓存键是完整历史消息的**精确字节序列**，命中价格约为未命中的 1/10。因此「往 prompt 里加东西」的第一问题不是加什么，而是**加在字节流的哪个位置**。三区模型把这件事写成规矩（[设计论证](prd/feature-hooks-injection.md) §2 四命题 + §4 决策）：

| 区 | 字节范围 | 内容 | 注入政策 |
|---|---|---|---|
| **cache-stable 稳定区** | 已提交历史的全部字节：系统提示词主体、tool schema、历史消息 | 系统提示词三段（rootTpl / AGENTS.md / skill manifest）、每条消息 | **绝对禁注入、禁改写**。没有任何 API 面向 hooks 开放此区；违反即架构错误（守护测试 `TestInjectionZones_ByteAssertion` 抓） |
| **cache-volatile 受控注入区** | 本 turn 前沿：Prepend 消息 + 用户文本 | 本 turn 新字节 | **唯一 per-turn 注入区**。memory 快照、后台子代理通知、`pre_prompt_inject` 授权 stdout、`/steer` 全部经 `PrePromptHook.Prepend` 单缝进入。字节一经提交即毕业入稳定区，永不再动 |
| **session-boundary 会话边界区** | 系统提示词内的会话常量槽位 | 日期（SessionDate）、agent types 清单、hooks 配置快照 | **仅会话边界重算**（进程启动 / `-resume` / `/new`），进程内冻结。变更 = 一次性前缀失效（已接受的代价） |

三律一句话：**只写未来、不改过去、启动冻结。**

- **为什么中段禁改写**：改写已发送过的任何字节，缓存命中截止到该位置，其后全部 token 按全价重算（尾长 × 0.9 每 turn）——「压缩旧消息」「往系统提示词塞动态内容」都属于此类，永不开放。
- **为什么前沿追加零代价**：追加不改变旧前缀，新字节只按 miss 计价**一次**，下一 turn 起沉淀为命中区。注入内容的**确定性不是缓存必要条件**（commit-once 由架构兜底）——非确定性输出的真实代价是 **token 预算**（每 turn 新字节全量入史），由 hook 作者自担。
- **唯一例外通道**：shell hook 的 stdout 默认**永不**进入 prompt（`pre_prompt` 是 observer，无注入管道）；唯一的授权例外是下节的 `pre_prompt_inject`。

## 2. `pre_prompt_inject` — 授权注入事件

**opt-in 默认无**。你配置它，签的是一份**内容信任**决策：「让这个外部命令的输出进入模型视野」——与 webfetch 的 URL gate 同类，不是缓存风险（通道 D 字节安全）。不是许可 shell 改写你的消息或系统提示词（显式否决，永不开放）。

```toml
# ~/.seek/hooks.toml 或 <project>/.seek/hooks.toml（项目级需 trust）
[[pre_prompt_inject]]
name = "ci-status"
command = "cat .seek/ci-context.txt 2>/dev/null"   # 惯用法：从 context 文件读静态内容
max_bytes = 2048                                   # 硬上限，默认 2048，上限 8192
```

语义（[PRD §4.5](prd/feature-hooks-injection.md)）：

- **执行时机**：每 turn PrePrompt 链内、in-process hooks（memory 快照、后台通知）**之后**；stdout 经 `<context type="hook" name="...">` 包裹后作为 user-role Prepend 消息注入（同一缝、FIFO 声明序，项目级先于用户级）。
- **有界**：`max_bytes` 硬截断 + 追加 ` [truncated]` 标记（wire-format，同 `SEEK_TOOL_RESULT` 截断先例）。注意截断按**字节**进行，多字节字符可能被切在边界上。
- **留痕**：audit log（`~/.seek/hooks-audit.jsonl`）每条注入记 `injected_bytes`（截断后实际入 prompt 的字节数）与截断原因；`seek hooks audit` 可查。
- **失败语义**：退出码 ≠ 0 / 超时 → **该条 stdout 丢弃、跳过、不阻断 turn**，下一条 hook 照跑（同 observer 降级纪律）。空 stdout 不产生空 `<context>` 块。
- **deny 面**：`pre_tool` 仍是唯一能 veto 的 shell 事件；inject 的所有「拒绝」路径（trust 拒绝、静态检查失败、执行失败）阻断的都是**注入本身**，不是回合。
- **确定性建议**：非确定性输出不破缓存（架构兜底），但每 turn 全量入史吃 token 预算——推荐从静态 context 文件读取，而不是每次现场生成动态输出。
- **子代理**：注入面随共享 Registry 下发到子代理（同 `pre_tool` 语义），子代理每次 Prompt 同样触发。
- **状态栏提示**：不做「本 turn 注入 N bytes」（M-O3.2 拍板）：字节量已沉淀在 transcript 的 `<context type="hook">` 块里、audit 有结构化计数，状态栏 per-turn 计数信息密度不抵其占用的常驻宽度（且状态事件链已在卡 c_z4mn6y 作为死代码移除，不为低密度指标复活写者）。

## 3. Hook 类型 / Hook kinds

两类 hooks，在类型层面区分，以便前缀缓存的安全性在编译时得到保证：

### 装饰器（Decorators）——可修改请求

| Hook | 触发时机 | 作用 |
|------|---------|------|
| `PrePromptHook` | 每轮 Prompt() 调用前，用户消息追加到历史之前 | 注入记忆、展开斜杠命令、改写用户文本（写面仅限前沿两字段 UserText + Prepend） |
| `PreToolUseHook` | 每轮工具调用执行前 | deny 工具调用、脱敏改写入参（唯一 deny 面，含 shell `pre_tool`） |

### 观察器（Observers）——只读

| Hook | 触发时机 | 作用 |
|------|---------|------|
| `PreTurn` | 每轮开始前 | 记录上下文（shell 事件 `pre_prompt` 落此——stdout 弃置） |
| `PostTurn` | 每轮结束后 | 分析结果、提炼记忆 |
| `PostToolUse` | 每个工具执行后 | 记录工具调用（无注入面——通道 E 已否决） |
| `SessionStart` / `SessionEnd` | 会话开始/结束时 | 初始化 / 清理、持久化 |
| `SubagentStart` / `SubagentStop` | 子代理启动 / 任一终态 | 后台编排审计 |
| `Stop` | 一次 Prompt() 回合整体落定 | 回合级审计（vs PostTurn 的每 model turn） |
| `PreCompact` | `/compact` 执行前 | 压缩审计 |
| `Notification` | OS 通知 / ask_user 弹出 | 通知审计 |

Observer 接口无返回值——签名本身保证它们碰不到 prompt 字节与控制流；deny 面**不随事件数增长**。

---

## 4. 内置 Hook / Built-in hooks

- **Checkpoint 安全网**（`checkpointHook`）——`OnPreTurn` + `OnSessionEnd` 检查和清理 checkpoint 状态
- **记忆系统**（`internal/memory`）——`PrePrompt` + `PostToolUse` + `SessionStart` + `SessionEnd` 实现 L/M/S 三层记忆（单缝的既有最大消费者）
- **后台完成通知**（`internal/subagent.Notifier`）——`PrePrompt` 将后台子代理结果注入父会话（父专属，子代理经 `CloneWithout` 剥离）
- **授权 shell 注入**（`internal/hooks.InjectRunner`）——`PrePrompt` 将 `pre_prompt_inject` 的 stdout 注入前沿（上节）

---

## 5. `seek hooks` CLI

```bash
seek hooks list                        # 合并视图 + 近 50 次平均耗时
seek hooks check --event <event>       # dry-run 哪些 hook 会触发（不执行）
seek hooks trust [--reset[=<path>]]    # 项目级 hook 信任管理
seek hooks audit [--since 24h] [--denied]  # 审计记录（含 injected_bytes）
```

事件名全集：`pre_tool` `post_tool` `pre_prompt` `session_start` `session_end` `subagent_start` `subagent_stop` `stop` `pre_compact` `notification` `pre_prompt_inject`。

---

## 6. 编写自定义 Hook（in-process）

```go
type myHook struct{}

func (h *myHook) OnPrePrompt(ctx context.Context, in hooks.PrePromptIn) (hooks.PrePromptOut, error) {
    // in.UserText — 用户原始文本；in.History — 只读快照
    return hooks.PrePromptOut{
        Prepend: []deepseek.Message{{
            Role:    deepseek.RoleUser,
            Content: "<context source=\"my-plugin\">\n...\n</context>",
        }},
    }, nil
}

registry := hooks.NewRegistry()
registry.Register(&myHook{}) // 自动分发到满足的所有 slot
```

写面纪律：只经 `UserText` / `Prepend` 两字段写前沿；注入内容用 `<context>` 包裹；确定**不改** `in.History`（未定义行为）。输出确定性仍推荐（省 token），但缓存正确性由 commit-once 架构兜底。

---

## 7. 设计要点 / Design notes

- **单一注入缝**——所有 per-turn 注入（memory / 通知 / 授权 stdout / steer）共用 `Prepend` 一条缝，让「注入必落前沿」成为一条断言可测的性质（`internal/hooks/zones_test.go`）
- **零侵入观察器**——observer 接口签名强制只读；shell hook 超时/失败只 audit 不阻断
- **deny 面单点**——`pre_tool` 是唯一 shell deny 点，审计与测试的杠杆
- **hook 脚本**——shell-level 钩子见 `seek hooks` CLI；项目级 hook 需 trust（sha256 追踪改动）
