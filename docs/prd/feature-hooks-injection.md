# Feature: hooks 注入面——受控注入区与缓存稳定区分离

**所属版本**：独立 feature（编排承重轴 P3-a，设计启动）
**前置阅读**：[`feature-orchestration.md`](feature-orchestration.md) §6（P3 骨架 D15–D17，本文是其设计细化）、[`feature-shell-hooks.md`](archive/feature-shell-hooks.md)（柱 B 红线原文）、[`v0.md`](archive/v0.md) §4.8.1（prefix-cache 字节纪律）、根 [`AGENTS.md`](../../AGENTS.md)「Token & prefix-cache constraints」
**状态**：📐 设计稿（2026-11-14，卡 c_o77s3m）→ ✅ **M-O3.2 已实现**（2026-11-17，卡 c_m2whz2）：`pre_prompt_inject` 授权事件全链路落地（hooksconfig 事件 + `max_bytes` 预算 + `InjectRunner` PrePromptHook 面（`internal/hooks/shell_inject.go`，注册于 in-process hooks 之后）+ audit `injected_bytes` 留痕）；三区模型守护测试 `internal/hooks/zones_test.go`（前段字节不变断言）+ 用户文档 `docs/guide-hooks.md` §1–2；§8 状态栏开放问题已拍板（不做）。M-O3.1（五观察点）此前已交付（卡 c_wsf7us）。
**定位**：把「注入内容改 prompt 字节会破 prefix-cache」这一张力**成文为规矩**——给出字节级缓存影响分析、全量生命周期点清单、注入通道的最终决策，以及 CC hooks 能力对照与逐项取舍。实现里程碑（M-O3.1/M-O3.2）沿用 orchestration §6 的排期，本文补全其设计依据。

---

## 1. 问题：注入能力与 prefix-cache 的字节张力

seek 的全部成本护城河建立在 DeepSeek 前缀缓存上：**缓存键是完整历史消息的精确字节序列，命中价格约为未命中的 1/10**（AGENTS.md「Token & prefix-cache constraints」；实测口径 `deepseek.Usage.PromptCacheHitTokens / PromptCacheMissTokens`，`pkg/deepseek/types.go:187`，状态栏已显示命中率）。因此任何「往 prompt 里加东西」的能力，第一问题不是加什么，而是**加在字节流的哪个位置**。

hooks 面的现状是一半规矩、一半沉默：

- **in-process 侧**（`internal/hooks`）：`PrePromptHook.Prepend` 是唯一注入口——user-role `<context>` 块、落在本 turn 前沿。v1 memory 子系统（快照 + delta）已在此运行两个版本。但「为什么只能这样、别的路径为什么不行」只在代码注释和 v3 PRD 的只言片语里，从未系统成文。
- **shell 侧**（`internal/hooks/shell_runner.go`）：`pre_prompt` **故意**降级为 `PreTurnObserver`——头注明引「PRD §2.1 + §3.2 forbid shell stdout from entering the prompt byte sequence」。红线是绝对的，但「显式授权也不行吗」的出口没画。

同时编排层的 P1（后台子代理完成通知）已经把注入缝当作**通知协议的载体**在用（orchestration D2），P3 又要新增 5 个观察点（D15）。注入面从「memory 的内部细节」长成了「编排层公共基础设施」，规矩必须先于第三个消费者立起来。

**本文回答三个问题**：

1. 每一种可能的注入通道，字节级缓存代价到底是什么（§2）？
2. 生命周期点全集长什么样，每个点能碰哪些字节（§3）？
3. 注入通道最终选哪条、否决哪些、授权边界画在哪（§4）；与 CC hooks 逐项对照后 seek 各取什么（§5）？

## 2. 字节级缓存影响分析

### 2.1 形式化：prompt 是一条单调增长的字节流

一个会话第 t 次 API 请求体是：

```
B_t = sys ‖ m_1 ‖ m_2 ‖ … ‖ m_{k_t}
```

- `sys`：系统提示词字节（`sysprompt.Compose` 输出——rootTpl + AGENTS.md 段 + skill manifest + 日期，`internal/sysprompt/sysprompt.go:138`）。
- `m_i`：历史消息序列化字节（JSONL 逐条对应，`schema_version=2`）。

DeepSeek 服务端按**最长公共前缀**匹配已见过的字节流（磁盘缓存，跨进程存活数小时）。定义：

- **前沿（frontier）**：`|B_t|` 处，即已发送字节流的末端。本 turn 新增的消息全部落在这里。
- **中段**：`0 < p < |B_t|` 的任意已发送位置。
- **位置 0**：`sys` 的起点。

三条基本命题，后面所有决策都从它们推出来。

### 2.2 命题一：中段改写 = 尾长全失效

若注入改变了位置 `p` 处已发送过的任何字节，则下一请求的缓存命中截止到 `p`，`p` 之后的全部 token 按全价重算：

```
每 turn 额外成本 ≈ (|B_t| - p) × 0.9 × 全价单价
```

**算例**（示意数量级）：中长会话 `sys` ≈ 10k token、历史 ≈ 60k token。若每 turn 往系统提示词里注入动态内容（位置 0 改写）：

- 每 turn：70k token 全部按 miss 计价（应只需为新增 token 付费）；
- 40 turn 会话累计 ≈ 2.5M token 的无效重付——**没有任何内容增量，纯粹为字节漂移买单**。

若改写发生在历史中段（比如「压缩」某条旧消息），代价 = 尾长 × 0.9，同样成立。这就是 AGENTS.md 红线「Never modify old messages before sending to the API」的定量形式。

**推论**：`pre_tool` 的 stdout 进 deny reason 不属于此类——deny reason 是**本 turn 新生成的 tool result 消息**，落前沿，不在中段。柱 B 把 stdout 放进 deny reason 一直是字节安全的；被禁的是它进入任何已存在消息。

### 2.3 命题二：前沿追加 = 零边际缓存代价

若注入只追加字节（`B_{t+1} = B_t ‖ a`，`a` 为新增），则 `B_t` 是 `B_{t+1}` 的严格前缀：

- 旧前缀全量命中，命中额与不注入时完全相同；
- `a` 本身按 miss 计价**一次**，下一 turn 起沉淀为命中区；
- 代价只剩 token 预算（`a` 永久占用历史），这是内容成本，不是缓存成本。

这就是 `PrePromptOut.Prepend` 的架构地位（`internal/hooks/hooks.go:61`、消费点 `pkg/agent/agent.go:489`）：Prepend 消息在用户消息**之前** append 进历史，永远在前沿。

**commit-once 不变式**：注入字节一旦 append 进历史，就**永不重算、永不改写**——它从前沿「毕业」进稳定区。三条既有机制共同保证这一点：

1. `Agent.appendMessage` 是唯一写历史的入口，append-only；
2. `ApplyPrePrompt` 每 `Prompt()` 只跑一次、在 turn 循环**之外**（`agent.go:483`）——流式重试重发的是同一批已提交消息，字节恒等；
3. `-resume` 从 transcript JSONL 折叠历史，**不重跑**当年产生注入的 hook（memory hook 在 resume 时重注快照，是「前沿重申」而非「中段改写」——新字节落新前沿，旧快照字节原样保留在历史里，`internal/memory/hook.go:482`）。

由此得到一个重要澄清（对 orchestration D17 的一处精确化，见 §4.5）：**注入内容的确定性不是缓存完整性的必要条件**。只要架构保证 commit-once + append-only，hook 输出每 turn 都不同也砸不了缓存——非确定性的真实代价在 token 预算（每 turn 新增字节全量入史）与上下文一致性（历史里堆着 N 份不同快照），是内容问题、由 hook 作者自担，不是缓存问题、由架构兜底。

### 2.4 命题三：会话边界重算 = 一次性失效

`sys` 在进程内冻结（`Header` 四字段「byte-stable across turns」，`sysprompt.go:122`；日期一次性捕获于 `cmd/seek/main.go:1410`）。变更只发生在进程启动 / `-resume` / `/new`（RebuildAgent）：

- 会话内：零缓存损伤；
- 跨进程：服务端缓存虽跨进程存活（同账号续用），`sys` 变了则前缀从位置 0 起失效**一次**——与 SessionDate 换日、AGENTS.md 改动同类，是已被接受的一次性代价（AGENTS.md：「`-resume` = new process = one accepted bust」）。

**推论**：系统提示词**尾部固定槽位**（若存在）在「会话内冻结」语义下是缓存无害的——它只能承载**会话常量**（agent types 清单、hooks 快照、日期），绝不能承载 per-turn 动态内容（那样每 turn 都是一次位置 0 改写，回到命题一的最坏情形）。这是 §4.3 决策的依据。

### 2.5 命题四：独立 tool_result 通道的协议事实

「把注入内容做成一条独立的 tool 消息」在 DeepSeek 线格式下**直接不可行**：

- tool-role 消息必须携带 `tool_call_id` 并应答紧邻其前的 assistant `tool_calls`（`pkg/deepseek/types.go:31`、`buildToolResultMsg`，`agent.go:1121`）。悬空 tool 消息会被 API 拒绝——坑 `986a485`（Esc 中断产生孤儿 `tool_calls` 后每 turn 报错，`docs/pitfalls.md`）是它的镜像面：**配对完整性是双向硬约束**。
- 合法变体只剩两个，各有归属：
  - **附身真实调用的结果**（CC `PostToolUse.additionalContext` 形态）：往本 turn 刚执行完的工具调用结果里**追加**字节。字节位置在前沿（结果尚未提交历史），追加本身安全；但需要新增一个「工具执行后改写结果」的 decorator 槽——PostToolUse 目前是 observer，无返回值。判定见 §4.4。
  - **后台通知伪装成 tool result**：模型没调用过任何工具，凭空出现结果语义不通；且它与「模型侧 poll 工具」是同一朵云（orchestration §3 不做什么 #3 已否决）。

### 2.6 通道代价总表

| 通道 | 字节位置 | 缓存代价 | 协议可行性 | 判定 |
|---|---|---|---|---|
| A. 改写系统提示词中段（含任意位置插入） | 0 | 每 turn 全量失效（§2.2） | — | **永不开放** |
| B. 系统提示词尾部固定槽位，会话内冻结 | 尾部，边界重算 | 会话内 0；跨进程一次性（§2.4） | 可行 | **收编为 session-boundary 区**：仅会话常量 |
| C. 改写/追加历史中段（压缩、孤儿 tool 消息） | `0<p<\|B_t\|` | 尾长 × 0.9（§2.2） | 孤儿 tool 消息被 API 拒（§2.5） | **不可行且不可取** |
| D. 前沿追加：user-role Prepend `<context>` | `\|B_t\|` | 0 边际（§2.3） | 已有（`ApplyPrePrompt`） | **唯一 per-turn 注入通道** |
| E. 附身真实工具结果追加（PostToolUse 注入） | 前沿（结果未提交） | 0 边际 | 需新 decorator 槽 | **P3 不做**，记开放问题（§4.4） |

可观测性佐证：以上每条命题都可以用状态栏命中率实测验证（`PromptCacheHitTokens/MissTokens` 已入 `deepseek.Usage`）——实现 M-O3.2 后跑一个「注入开启 vs 关闭」的 A/B 冒烟，命中率曲线应无差异（通道 D），这是 §7 验收 #5 的雏形。

## 3. 生命周期点清单

### 3.1 现有 in-process 点（`internal/hooks`，7 个）

| 点 | 类型 | 触发时机（代码位置） | 字节接触面 |
|---|---|---|---|
| `PrePromptHook` | decorator | 每 `Prompt()` 一次，用户消息 append 前（`agent.go:483`） | **前沿写**：`UserText` 改写 + `Prepend []Message` |
| `PreToolUseHook` | decorator | 每次工具调用前（`agent.go` dispatch） | 无 prompt 字节；`Args` 仅限脱敏改写（未来历史的前沿字节）；`Deny` 生成本 turn 新 tool result |
| `PreTurnObserver` | observer | 每 model turn 开始（`agent.go:521`） | 无（只读 History 快照） |
| `PostTurnObserver` | observer | 每 model turn 结束（`agent.go:632/696`） | 无 |
| `PostToolUseObserver` | observer | 每次工具调用后 | 无（Result 已定格） |
| `SessionStartObserver` | observer | 启动装配完成后（`cmd/seek/main.go:1844`） | 无 |
| `SessionEndObserver` | observer | 进程退出（`main.go:1851`） | 无 |

类型级隔离是本表的第二列：**decorator 与 observer 在 Go 接口签名上分离**，observer 无返回值即无法触碰字节（`hooks.go` 包头注释：「prefix-cache safety is a compile-time property」）。这不是约定而是编译期性质，新点必须维持。

### 3.2 现有 shell 事件（hooks.toml，5 个）

| 事件 | 实现 | 语义 | 字节面 |
|---|---|---|---|
| `pre_tool` | PreToolUseHook | 退出码 ≠ 0 → deny，stdout 进 deny reason | 前沿（新 tool result） |
| `post_tool` | PostToolUseObserver | 仅 audit | 无 |
| `pre_prompt` | **PreTurnObserver（故意降级）** | 仅 audit；stdout 弃置 | 无 |
| `session_start` | SessionStartObserver | 仅 audit | 无 |
| `session_end` | SessionEndObserver | 仅 audit | 无 |

### 3.3 P3 新增观察点（D15 细化，全部 observer、无 deny）

| 新点 | in-process 接口 | 触发时机（落点） | shell 事件 | 事件 env（在通用 `SEEK_*` 基础上新增） |
|---|---|---|---|---|
| `SubagentStart` | `SubagentStartObserver` | Manager 发 `started` 事件后（`internal/subagent/manager.go` step 7，sync+background 同） | `[[subagent_start]]` | `SEEK_SUBAGENT_SID` / `SEEK_SUBAGENT_TYPE` / `SEEK_SUBAGENT_DESC` |
| `SubagentStop` | `SubagentStopObserver` | 任一终态事件落盘后（completed / failed / killed / orphaned） | `[[subagent_stop]]` | 上者 + `SEEK_SUBAGENT_STATUS` / `SEEK_SUBAGENT_TURNS` / `SEEK_SUBAGENT_TOKENS` |
| `Stop` | `StopObserver` | `Agent.Prompt` goroutine 落定（final answer / max turns / ctx cancel / fatal 任一终态后、`close(out)` 前） | `[[stop]]` | `SEEK_STOP_TURNS` / `SEEK_STOP_FINISH` |
| `PreCompact` | `PreCompactObserver` | `/compact` 执行前（TUI `cmdCompact` 发起处、LLM 调用前） | `[[pre_compact]]` | `SEEK_COMPACT_TRIGGER`（manual） |
| `Notification` | `NotificationObserver` | OS 通知发出（柱 H `Notifier` 调用点）/ `ask_user` picker 弹出 | `[[notification]]` | `SEEK_NOTIFY_KIND` / `SEEK_NOTIFY_TITLE` |

分层说明：`Stop` 与 `PostTurn` 是两个粒度——`PostTurn` 每 model turn 一次（一次 `Prompt()` 内可多次），`Stop` 一次回合（`Prompt()` 整体）一次。命名沿用 CC 的 `Stop` 而不是 `PostPrompt`，因为「回合落定」的直觉来自「agent 停下来了」，与 `SubagentStop` 对仗。

audit log 每行新增 `subagent_sid` 字段（柱 G §5.3 已预留口径）；全部 observer-only：shell hook 超时只 log 不 deny（现有 observer 语义），deny 面不扩——`pre_tool` 仍是**唯一** shell deny 点（§5.3 取舍）。

### 3.4 新增授权注入事件（D17 细化，见 §4.5）

| 事件 | 实现 | 语义 | 字节面 |
|---|---|---|---|
| `pre_prompt_inject` | **PrePromptHook**（ShellRunner 第二实现面） | 每 turn PrePrompt 时执行，stdout 经 `<context>` 包裹后进 Prepend | 前沿（同通道 D） |

这是 shell 面第一个获得「写前沿」授权的事件，opt-in 默认无、有 `max_bytes` 硬上限、audit 记录注入字节数。设计细节与责任边界在 §4.5。

## 4. 注入通道决策：三区模型 + 唯一缝

### 4.1 三区定义（对 orchestration D16 的字节级落地）

| 区 | 字节范围 | 内容 | 注入政策 |
|---|---|---|---|
| **cache-stable 稳定区** | 已提交历史的全部字节：`sys` + `m_1…m_{k}` | 系统提示词三段（rootTpl / AGENTS.md / skill manifest）、tool schema bytes、历史消息 | **绝对禁注入、禁改写**。无任何 API 面向 hooks 开放此区；违反即架构错误（会被 §4.6 守护测试抓住）。 |
| **cache-volatile 受控注入区** | 本 turn 前沿：Prepend 消息 + UserText（含 modeReminder 尾缀） | 本 turn 新字节 | **唯一 per-turn 注入区**。所有动态注入（memory 快照/delta、P1 后台通知、授权 shell stdout、`/steer`）经 `PrePromptHook.Prepend` 单缝进入。字节一经提交即毕业入稳定区，永不再动。 |
| **session-boundary 会话边界区** | `sys` 内的会话常量槽位 | SessionDate、agent types 清单（P2 入 schema/description）、hooks 配置快照 | **仅会话边界重算**（进程启动 / `-resume` / `/new`），进程内冻结。变更 = 一次性失效（§2.4，已接受）。 |

三律一句话（同 orchestration D16）：**只写未来、不改过去、启动冻结**。

### 4.2 决策

> **per-turn 动态注入 → 只走受控注入区（user-role Prepend `<context>`，通道 D）；会话常量注入 → 只走 session-boundary 区（通道 B）；其余一切通道（改写 `sys` 中段、改写历史、悬空 tool 消息）永久关闭。**

授权出口唯一化：shell 面通过显式 opt-in 的 `pre_prompt_inject` 进入同一缝（§4.5）。**不设第二条 per-turn 注入缝。**

### 4.3 为什么尾部固定槽位不承载 per-turn 内容（通道 B 的边界）

卡片设想的「系统提示词尾部固定槽位」在字节上是可行的，但可行性完全依赖**内容冻结**：

- 槽位在 `sys` 内 ⇒ 任何变更都是位置 0 改写 ⇒ 命题一最坏情形（每 turn 全量失效）；
- 因此槽位内容必须是**会话常量的纯函数**（启动时的注册表、配置、日期），进程内不可变；
- 而会话常量已经有更好的既有出口——P2 的 agent types 进 enum/description（一次性 schema 变更）、hooks 快照进启动装配——它们天然是 session-boundary 区成员。

结论：尾部槽位**不需要新机制**，它就是 session-boundary 区的几何描述。新增一个「可热更的尾部槽」会将通道 A 披着 B 的外衣复活，否决。

### 4.4 为什么不做独立 tool_result 通道（通道 E 的搁置）

- **悬空 tool 消息**：协议不可行（§2.5），无讨论余地。
- **PostToolUse 结果追加**：字节安全（前沿），但需要把 PostToolUse 从 observer 升级为 decorator——打破「observer 无返回值」的编译期安全性质，且引入第二个注入缝。其目标用例（「edit 后自动把 lint 结果喂给模型」）在 seek 里模型可自跑 lint，无硬需求。**P3 否决，记 §8 开放问题**；若未来确需，须满足：decorator 只能在结果字节尾追加（不允许改写已有字节）+ 独立审计计数。
- **通知伪装 tool result**：随模型侧 poll 工具一并否决（orchestration §3 #3）。

### 4.5 `pre_prompt_inject`：授权、有界、留痕（D17 细化 + 两处精确化）

红线**精确化而非推翻**：feature-shell-hooks §2.1 的「shell hooks 的 stdout 不进 prompt」收紧为「**未被显式授权的** shell stdout 永不进 prompt」。授权通道全貌：

```toml
[[pre_prompt_inject]]
name = "ci-status"
command = "cat .seek/ci-context.txt 2>/dev/null"   # 惯用法：从 context 文件读静态内容
max_bytes = 2048                                   # 硬上限，默认 2048，上限 8192
```

- **执行时机**：每 turn PrePrompt 链内、in-process hooks 之后，stdout 经 `<context type="hook" name="...">` 包裹后并入 Prepend（同一缝、同一 FIFO 序）。
- **有界**：`max_bytes` 硬截断 + 追加截断标记（wire-format，同 `SEEK_TOOL_RESULT` 的 ` [truncated]` 先例）；多条 hook 按 toml 声明序拼接。
- **留痕**：audit log 记录每条注入的 name、字节数、是否截断。
- **失败语义**：退出码 ≠ 0 / 超时 → 本条跳过 + 状态栏提示，**不阻断 turn**（同 observer 降级路径，柱 B §3.4 纪律）。

对 D17 的两处**精确化**（本设计稿修正，回写 orchestration 的衔接点）：

1. **非确定性的真实代价面**。D17 原文「非确定性输出 = 每 turn cache 半失效」表述不准：由 §2.3 的 commit-once 论证，注入输出无论确定与否都**砸不了缓存**（历史 append-only 由架构保证）。真实代价是 **token 预算**（每 turn 新字节全量入史）与**上下文一致性**（历史堆叠 N 份不同快照）。文档措辞改为：「非确定性输出不破缓存（架构兜底），但每 turn 全量入史，token 成本自担；推荐从静态 context 文件读取」。
2. **授权的对象是「风险签名」而非字节风险**。用户 opt-in 签名的是「让外部进程的内容进入模型视野」这一内容信任决策（prompt injection 面），而非缓存风险——缓存风险在通道 D 上为零，无需用户承担。这使授权语义与 webfetch 的 URL gate 同类（内容信任），不与 cache 纪律混淆。

### 4.6 守护机制（让规矩可执行）

1. **编译期**：注入能力只存在于 `PrePromptOut{UserText, Prepend}` 两个前沿字段；不新增任何触及 `sys` / 历史的 API。`pre_prompt_inject` 的 ShellRunner 实现复用同缝，无旁路。
2. **测试期**（M-O3.2 验收，orchestration P3 #5 雏形的具体化）：
   - hook 执行前后 `sys` 字节 hash 不变、tool schema bytes hash 不变；
   - 注入开启/关闭的两次会话，逐 turn `PromptCacheHitTokens` 曲线无差异（A/B 冒烟，§2.6）；
   - `pre_prompt_inject` 超 `max_bytes` 截断 + 标记；历史消息前后 diff 只增本 turn。
3. **运行期**：audit log 的注入字节数可聚合；状态栏「本 turn 注入 N bytes」提示是否上（orchestration §9 开放问题），M-O3.2 实现时以信息密度定夺。

### 4.7 缝的现有与既定消费者（单一缝的负荷核对）

| 消费者 | 注入物 | 状态 |
|---|---|---|
| memory 子系统 | `<context type="memory.soul">` 快照 / `memory.delta` 增量 | 已交付（v1） |
| `/steer` 用户插话 | `<context>` 包裹的 steer 文本 | 已交付 |
| P1 后台子代理通知 | `<context>[agent: background done …]`（resume 回灌同缝） | 设计既定（orchestration D2/D3） |
| P3 `pre_prompt_inject` | `<context type="hook">` 授权 stdout | 本文 |
| suggested-reply 校准注记 | `<context>` system note | 已交付（柱 D） |

四类已运行 + 两类既定消费者共用一缝两年无冲突，证明缝的吞吐与语义（「user-role、`<context>` 包裹、一 turn 一批、FIFO」）足够通用。

## 5. CC hooks 能力对照与 seek 取舍

### 5.1 证据口径

CC 侧 = Claude Code 官方 hooks 文档（code.claude.com/docs/en/hooks，2026-11-14 核对）+ ZCode（CC 形态 harness）系统提示/schema 原文（orchestration §2 同源证据链）。seek 侧 = 本仓主干代码。对照是**能力面定性**，不是承诺清单。

### 5.2 生命周期点对照

| CC 事件 | CC 能力（文档实测） | seek 对应点 | 差异取舍 |
|---|---|---|---|
| SessionStart | additionalContext 注入会话开头；resume/clear/compact 时重跑 | `SessionStartObserver`（observer）+ memory 快照经 Prepend 在**首个 PrePrompt** 注入 | 形态不同、字节等效：CC 注入点在会话开头，seek 的首 turn Prepend 同样落前沿；seek 不给 SessionStart 直接注入能力，统一走 Prepend 缝 |
| UserPromptSubmit | additionalContext 随 prompt 注入；stdout 即上下文；exit 2 拒绝 prompt | `PrePromptHook`（decorator，唯一缝）+ P3 `pre_prompt_inject` | 注入对齐（都落前沿）；**拒绝用户输入不做**——seek 是 turn-based CLI，用户即输入者，hook 拦用户消息语义不通 |
| PreToolUse | permissionDecision allow/deny/ask/defer + `updatedInput` 改写工具入参 | `PreToolUseHook`：`Deny`（唯一 deny 面，含 shell `pre_tool`）+ `Args` 仅限脱敏 | deny 对齐；ask/defer 不做（permission.Policy 的 askFn 已有 ask 语义，hook 无需重复）；**通用 args 改写不做**（v3 已否决中间件化，仅保留 redact 白名单用途） |
| PostToolUse | additionalContext 附在 tool result 旁 | `PostToolUseObserver`（observer） | **注入不做**（§4.4 通道 E 否决）；退出码/stdout 仅入 audit |
| Stop | exit 2 阻止停止、强制继续；additionalContext 回注 | P3 新增 `StopObserver`（observer） | **阻断续跑不做**：违背用户显式停止意图，且「强制继续」在 turn-based 模型里等于替用户发起 turn，越权 |
| SubagentStop | 同 Stop（子代理粒度） | P3 新增 `SubagentStopObserver`（observer） | 同上，仅观测 |
| PreCompact | stdout 成为 compaction 摘要指令；exit 2 阻断 compact | P3 新增 `PreCompactObserver`（observer） | **摘要指令改写不做**：compact 本身是用户显式触发的一次性历史重写（已接受 bust），再开放指令注入徒增不可控面；观测（审计谁在何时压缩）已够用 |
| Notification | terminalSequence 桌面通知等；不可阻断 | P3 新增 `NotificationObserver`（observer） | 对齐；OS 通知本身柱 H 已交付 |
| SessionEnd | 不可阻断；共享 1.5s 超时预算 | `SessionEndObserver` | 对齐（同步执行保 audit 落盘） |
| PermissionRequest | decision.behavior allow/deny 应答权限询问 | 无对应点（permission.Policy 内置 askFn） | **不做**：seek 的权限决策走 ask_user TUI 面向真人，不开放给 hook 应答（hook 与用户抢权限决定权是安全反模式） |

### 5.3 横切能力对照

| 能力面 | CC | seek 取舍 | 理由 |
|---|---|---|---|
| 注入缝数量 | ≥5 个事件可注入（additionalContext 扇出） | **1 个**（Prepend）+ 授权 shell 通道复用同缝 | 多缝 = 多字节漂移面 + 多审计点；单缝让「注入必落前沿」成为可一条断言测完的性质 |
| 注入落点 | harness 内部装配（随 prompt / tool result / 会话头） | 显式三区模型 + 前沿唯一 | seek 的缓存是字节精确匹配，装配纪律必须成文；CC 的 API 缓存按 TTL 计费衰减，容忍度高一个量级 |
| 注入长度上限 | 10,000 字符（超限落盘 + 2k 预览） | `max_bytes` 默认 2048、上限 8192、截断标记 | 同向设计；seek 取更小默认——注入字节永久入史，默认应保守 |
| hook 失败语义 | `onFailure: "block"` 可选阻断 | **失败一律降级不阻断**（柱 B §3.4） | 无人值守纪律：hook 坏不能锁死会话；pre_tool 的 deny-on-fail 是唯一例外且方向是「拦」不是「锁」 |
| 阻断类能力 | UserPromptSubmit 拒 prompt、Stop/SubagentStop 强续、PreCompact 阻断 | 全部不做；deny 面收敛于 `pre_tool` | 见 §5.2 各行；deny 面单点是审计与测试的杠杆 |
| 确定性要求 | 无显式要求（TTL 缓存兜底） | 注入内容确定性由架构兜底（commit-once），非 hook 作者责任；token 代价自担（§4.5 精确化 1） | 字节精确缓存下把确定性外包给用户必然翻车；seek 选择架构保证 |

### 5.4 为什么 CC 能宽、seek 必须严

CC 的注入扇出依托 Anthropic prompt caching 的 **TTL 模型**：缓存按时间窗存活，字节漂移的惩罚是「重新写一次缓存」（一次 miss 后恢复），且 harness 常驻、缓存断点由 `cache_control` 显式管理。DeepSeek 的缓存是**字节精确的隐式前缀匹配**：一个字节变，其后全部重付，没有优雅降级。同一句「往上下文里塞点东西」，CC 的边际成本是一次 re-cache，seek 的边际成本是全历史 × 0.9。所以 seek 的注入面必须比 CC **更收敛、更晚绑定、更单一缝**——这不是功能差距，是成本模型的差异（orchestration §1「成本纪律是领先面」在 hooks 面的具体化）。

## 6. 不做什么（边界重申）

1. ❌ **系统提示词任何位置的 per-turn / 运行时注入**——通道 A/B 边界见 §4.3。
2. ❌ **历史消息改写 / 压缩 / 孤儿 tool 消息**——通道 C（§2.2/§2.5）。
3. ❌ **PostToolUse 结果注入 decorator**——通道 E，P3 否决记开放问题（§4.4）。
4. ❌ **UserPromptSubmit 式拒绝用户输入、Stop 式强制续跑、PreCompact 式阻断压缩**（§5.2）。
5. ❌ **hook 应答权限询问**（PermissionRequest 等价物）——权限决策面向真人（§5.2）。
6. ❌ **通用工具参数改写**——维持 v3 柱 B 边界，`Args` 仅限脱敏。
7. ❌ **hooks 通用 args 改写 / CC 插件市场**——维持 orchestration §3 #5 边界。

## 7. 实现期验收（M-O3.1 / M-O3.2 用，本卡不执行）

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | §3.3 五个新观察点全部无返回值、超时只 log；deny 面未扩（`pre_tool` 仍是唯一 deny 点） | 单元测试 + 代码审查（observer 接口签名） |
| 2 | 新 shell 事件 env 齐全（`SEEK_SUBAGENT_SID` 等）；audit 行含 `subagent_sid` | 集成测试（fake hook 脚本写临时文件） |
| 3 | `pre_prompt_inject` 输出进本 turn Prepend `<context type="hook">`；>max_bytes 截断 + 标记；历史消息前后 diff 只增本 turn | 集成测试（前后 messages 字节对比） |
| 4 | 未配置 `pre_prompt_inject` 时 PrePrompt 字节路径与现状一致 | 现有测试全绿（零回归） |
| 5 | hook 执行前后 `sys` 与 tool schema bytes hash 不变；注入开/关 A/B 冒烟的逐 turn `PromptCacheHitTokens` 无差异 | 集成测试（hash 断言 + fake 后端 usage 断言） |
| 6 | 三区模型写入 guide-hooks.md（用户文档） | 文档评审 |

## 8. 开放问题

- **PostToolUse 结果追加注入**（通道 E）：若 P1 后台通知落地后有「工具结果旁提示」的强需求再评估；前提是 decorator 限定「只准尾追、不准改写」。
- ~~**状态栏注入字节数提示**（orchestration §9 遗留）：M-O3.2 实现时以信息密度定夺。~~ **已拍板（M-O3.2，2026-11-17）：不做。** 字节量已沉淀在 transcript 的 `<context type="hook">` 块、audit 有结构化 `injected_bytes` 可聚合；状态栏 per-turn 计数信息密度不抵常驻宽度，且状态事件链已作为死代码移除（卡 c_z4mn6y），不为低密度指标复活写者。失败提示同 observer 纪律走 audit + 启动 stderr。
- **`pre_prompt_inject` 的 per-hook 条件触发**（如仅特定子目录 / 仅 plan 模式）：P3 最小形态不做 matcher，实战反馈再议（柱 B 当年也是先 `tool` 精确匹配后 glob 的教训）。

## 9. 与已交付面的关系

| 已交付面 | 本文动作 |
|---|---|
| `internal/hooks` Registry 七点 + 编译期 decorator/observer 分离 | **扩展不重写**：五个新 observer 走 `Register` 现有分发；ShellRunner 加事件不改既有五事件语义 |
| feature-shell-hooks「stdout 禁入 prompt」红线 | **精确化不推翻**：未授权永不；授权通道有界留痕（§4.5） |
| v1 memory Prepend 注入 / 柱 D 校准注记 | **确认既定**：它们是单缝的既有消费者，本文把其字节安全性论证补齐（§2.3/§4.7） |
| orchestration D15/D16/D17 | **细化 + 两处精确化**：D17 非确定性代价表述修正、授权语义澄清（§4.5）；D16 三区落到字节范围定义（§4.1） |
| v0 §4.8.1 / AGENTS.md 缓存三条红线 | **不变**：本文是它们的注入面展开，无一放松 |
