# 评估笔记：事件溯源 fold 不变量在 seek 的适用性 —— approval 与 todo

> **状态**：📝 评估完成，只评估不重构 · 2026-11-12
> **出处**：`docs/dsh-analysis.md` §2 / §8 / §9.2 / §9.4 —— dsh 把「状态 = 事件投影」推到 approval/todo/schedule 全系统，seek 只在 plan 子系统。本笔记回答：seek 的 approval（运行时 Policy + 转录重建混合）与 todo 若 fold 化，收益与代价各是什么。
> **结论先行**：approval 全量 fold **不值得**；**部分做**一条（resume 恢复 batch/preApproved，读已有 marker 后缀即可）；todo **无需任何动作**（已由 plan 子系统承担且是 fold 正样板）。

---

## 0. 结论速览

| 对象 | 结论 | 一句话理由 |
|---|---|---|
| approval 全量 fold 化 | **不值得** | dsh fold 恢复的东西（策略位）seek 用 session header 已覆盖；其余审批状态的转换触发器是 TUI 事件，**根本不在转录里**，fold 无法表达 |
| preApproved/batch 的 resume 缝隙 | **部分做**（最小修复） | 信息已经在转录里（`[plan: approved] (auto-approve-per-step)` 后缀），reconstruct 只匹配前缀把它丢了；现状下 resume 后模型的 re-arm 契约失效 |
| todo | **无需动作** | seek 没有 todo 工具；清单角色由 plan steps 承担，而它**已经是** fold。若将来加 dsh 式「模型自管 todo」，照 plan 形状做，零新机制 |

---

## 1. 现状：seek 的审批 / 清单状态分别存在哪

### 1.1 approval 侧：四块状态，四种存法

| 状态 | 运行时真身 | 持久化 / resume | 出处 |
|---|---|---|---|
| `pref`（Preference 单 bit） | `Policy.pref` | **session header 投影**：TUI 每次持久化由 `persistSession` 回写 `Yolo = m.opts.Yolo`（`update_agent.go:771`）；resume 时 `main.go:903-907` 读回 | `permission.go:196` |
| `workflow`（Workflow 轴） | `Policy.workflow` | **header bool 投影（有漂移，见下）**：resume 时 `main.go:908-912` 读 `loaded.Plan` → 至多回到 `PlanAnalyze`；PlanExecute 由 propose 事件在运行时进入（`main.go:2007`） | `permission.go:197` |
| `preApproved` + `bridge.batch` | `Policy.exec.preApproved` + planBridge.batch | **运行时字段 + 半重建**：reconstruct 恢复 steps/currentIdx（`main.go:1615`），**batch 位不恢复**（见 §6.2） | `permission.go:248` |
| `alwaysAllow`（per-Kind 会话授权） | `Policy.alwaysAllow` | **刻意易失**：明文设计「从不持久化（resume 后重新询问）」（PRD §十一表格）；SetPref/SetWorkflow 清空 | `permission.go:221` |

这就是 dsh-analysis §2 说的「混合态」的具体形状：pref 是「运行时真身 + header 快照投影」，workflow/preApproved 是「运行时真身 + 部分转录重建」，alwaysAllow 是纯易失。

> **顺带发现（本笔记评估中坐实，不在此修）**：header 投影的漂移不是理论风险——TUI 的 `persistSession`（`update_agent.go:759-780`）回写 `Yolo` 却**从不回写 `Plan`**，全 `internal/tui/` 无任何 `Session.Plan` 写入点。后果：TUI 运行中 `/plan` on → 退出 → resume，`loaded.Plan` 仍是启动时的值（false），resume 落在 WorkflowNone 而 planTool 却带着转录重建的 steps。行为无害（steps 只是面板 + 工具内存态），但「workflow 的 header 投影」实际上从不反映运行中变化。这与 §6.2 是同族问题——**该进 header / 该读 marker 的信息没有走完链路**——修法同属行级（persistSession 补一行 `m.opts.Session.Plan = m.opts.Plan`），留给后续单独处理。

### 1.2 todo 侧：没有 todo 工具，plan steps 就是清单

`internal/tools/` 下没有 todo 工具（grep 全仓无命中）。seek 的「任务清单」由 plan 子系统承担：propose 产出 steps、用户审批、plan(start/complete/skip) 推进——而它**已经是 fold 正样板**：`plan/reconstruct.go:47` 从转录重放 propose 参数与 plan 调用，无镜像文件、无并行运行时字段（步骤状态本身）。

dsh 的 todo 是另一个物种：「模型自管清单」——`todo_write` 整表替换、投影在下一 turn/start 清空（dsh-analysis §6），不经人类审批。seek 若将来要这个物种，见 §6.3。

### 1.3 一个先说清的事实：审批交互不落转录

审批发生在 TUI ↔ permission 的 channel 上（`ApprovalRequest`，`permission.go:129`）：用户按 y / N / [a] 的**那一刻**不产生任何转录行。转录里能观察到的是间接信号：deny → `ErrDenied` 文本作为 tool result 落转录；allow → 只有工具正常输出（与 yolo 授权无法区分）；[a] 授予 → 完全不可观察。**dsh 有专门的 `approval/asked|decided|policy` 事件；seek 的事件源里根本没有这类事件的容身处**——这是 §4 一切代价的起点。

---

## 2. dsh 对照：它的 approval/todo fold 到底恢复了什么

dsh `approval/*` fold 在 resume 时恢复的，拆开看是两样东西：

1. **策略位**（`approval/policy` / `permission/preset`，取值 ask/never）——seek 对应物是 `pref`，而它已经跨 resume 存活（header 投影）。**dsh 需要 fold 是因为它的日志是唯一事实源、没有 header 这种东西；seek 的 header（JSONL 第 1 行标量元数据）本来就是单 bit 状态的合法持久化位置。** 这条 sit 在 §9.2 否决的核心上，permission-refactor 之后依然成立。
2. **授权历史**（asked/decided 逐条）——seek 对应物是 alwaysAllow 与逐次 y/N。alwaysAllow 的易失是明文安全设计（「a grant must not silently outlive the context it was given in」，`permission.go:214-220`）；逐次 y/N 的历史对本地单人 CLI 的审计增量趋零（§3.2）。

todo 一侧两边已同构：dsh `todo/write` 事件 fold ↔ seek plan steps 转录重建，事件都来自 LLM 工具调用。

---

## 3. 收益逐项评估

### 3.1 resume 一致性

- **pref**：已覆盖，无收益。
- **alwaysAllow**：fold 化「能」恢复它——但这恰是要撤销一个明文安全设计。不收益，反受损。
- **preApproved/batch**：有**真缝隙**，但缝隙不在「需要 fold 机制」，而在「已有信息没读全」——信息就在转录 marker 后缀里（§6.2 详述）。修复是行级改动，不需要事件溯源。
- **workflow**：resume 至多回到 PlanAnalyze（header 只有 bool，且按 §1.1 顺带发现该 bool 在 TUI 下还可能滞后）。理论上可从 `[plan: approved]` marker fold 出 PlanExecute，但恢复到「进 execute 前需要用户重新看见」的保守态与 alwaysAllow 的易失逻辑同构，是合理的安全默认，不判为缝隙。

### 3.2 审计

转录已经记录了审计真正关心的两样：每次 deny（ErrDenied 文本）与每次已执行的副作用（tool result + checkpoint）。「用户点了 y」这个中间事件对本地单人 CLI 的增量信息≈0——用户就是操作者，y 的动机不存在第三方要追责的对象。dsh 的字节级审计重放是**模型公司**的需求（「模型当时到底看到了什么」，dsh-analysis §11.3），不是单机 CLI 用户的需求。

### 3.3 replay

同上：dsh 的可重建性 headline 服务于模型开发调试。seek 的 replay 场景（`-resume`）只需要**状态**正确，不需要逐字节重建请求——而 seek 的请求前缀稳定性已由约定式纪律 + `TestCompose_IsDeterministic` 守卫达成（dsh-analysis §8.2 承认「结果等价」）。

---

## 4. 代价逐项评估

### 4.1 根本代价：事件源缺失，fold 无法表达 TUI 触发的转换

把 approval 全部状态变更列出来看触发器：

| 变更 | 触发器 | 在转录里吗 |
|---|---|---|
| pref 变更 | `/yolo` 命令、启动 flag | ✗（TUI 命令） |
| alwaysAllow 授予 | 审批按键 [a] | ✗ |
| preApproved 失效 | plan(complete/skip) | ✓ |
| preApproved 失效 | **Esc 中断**、`/plan` off、SetWorkflow 转换 | ✗（TUI 事件） |

一半以上的转换触发器是 TUI 事件。**这同时重新定性了 §9.4 对 preApproved 防御性重置链的批评**：那串「complete/skip、Esc、/plan off、每次 SetWorkflow 都要清零」的利息，根因不是「忘了 fold」，而是**状态机的触发器根本不在事件源里**——fold 化消不掉它，因为 fold 源（转录）里没有 Esc。运行时显式清零是对这个事实的诚实回应。真正错误的只有一种形态：既持有运行时字段、又假装它能从转录完整重建（两个真相源）；seek 目前没有犯这个错（reconstruct 不碰 preApproved，两边的管辖域是分清的）。

### 4.2 格式代价：schema v2 的转录是纯消息流

seek 的 session JSONL（schema v2）第 1 行 header + 之后**每行一条 `deepseek.Message`**，转录 = 模型可见面，两层没有分离。dsh 的 `approval/*` 之所以能落日志又不干扰模型，靠的是事件流与 surface 的二值分离（`deriveEventMessage` 显式排除，dsh-analysis §2）。seek 要给审批事件找容身处，只有两条路：

- **加非 Message 事件行**（schema v3）：session.Load / Repair / fork / loadMeta / 一切把 `lines[1:]` 当 Messages 的消费路径都要学会跳过；从此「转录里有但模型看不到」成为 seek 也要维护的复杂度（dsh §10 警告的第 2 条）。这是地基级变更。
- **marker 塞进 tool result 文本**：append-only 不改历史，但把审批事件变成模型可见内容——模型会学到「用户授权过什么」，轻微扩大 prompt 面的操纵空间，且每次审批多花 token。

### 4.3 缓存风险：比直觉温和，但集中在消费面

前缀缓存的关键是**不改已发送的旧字节**；append-only 的新行（无论 Message 还是事件行）本身不 bust 缓存，resume 本来就是「一次已接受的 bust」。所以 fold 化的缓存风险不在「多记事件」，而在**改坏历史的风险面扩大**：Repair 重写、fork 复制、压缩改写这些碰历史行的路径，每多一种行类型就多一个「误把事件行编进请求 / 误删事件行」的出错点。dsh 用独立的 `Session` 重建每个请求来自证（invariant 伴生插件），seek 若走这条路还得抄这套自证——为一个收益≈0 的变更（§3）引入两层机制。

### 4.4 安全设计冲突

fold 的 resume 语义是「忠实恢复授权上下文」，而 seek 的审批安全姿态是「易失优先」（alwaysAllow 不持久化、preApproved 不继承进 Spawn、grant 不越上下文存活）。两者方向相反：全量 fold 化不是中性重构，是对安全默认的系统性放松。

---

## 5. 提炼判据：fold 的成本由「事件是否本来就在转录里」决定

综合 §1-§4，判据可以压成一句话：

> **状态的生产者是 LLM 工具调用 → 事件天然在转录里 → fold 免费（plan 是正样板，todo 若引入也在此列）；状态的生产者是 TUI/用户交互 → 事件不在转录里 → fold 要先发明事件行 + 引入转录/模型面分离 + 与易失安全默认打架 → 贵且负收益（approval 在此列）。**

这是对 §9.4 纪律（「先问能不能从转录 fold 出来」）的补强而非修改：纪律问的是「加状态前」的取舍方向，本判据给的是「答不了」时的成本解释。dsh 自己也站在这条线的同一侧——它的 schedule fold 成立的前提是 session-local（§9.4 边界小节），跨进程的编排状态两边都不 fold。

---

## 6. 分项结论

### 6.1 approval 全量 fold 化：不值得

三条理由，每条独立成立：**(a)** dsh fold 恢复的策略位，seek 的 header 投影已覆盖，买不到可观察变化（§2.1，维持 §9.2 否决）；**(b)** 其余审批状态的一半转换触发器不在事件源里，fold 在语义上就不完备，只能表达半个状态机（§4.1）；**(c)** 完整表达需要 schema v3 + 消费面改造 + surface 分离 + 自证机制，换来的审计/replay 收益对本地单人 CLI 趋零（§3.2/§3.3/§4.2/§4.3）。

### 6.2 部分做：resume 恢复 batch / preApproved（最小修复，建议实施）

**缝隙**：propose 被批为 auto-approve-per-step 时，marker 写的是 `[plan: approved] (auto-approve-per-step)`（`propose.go:344`），且同一结果文本教模型「Esc 后经 plan(start) re-arm」。但 `reconstruct.go:54` 只 `HasPrefix(m.Content, approvedMarker)` 就把后缀丢弃——resume 后 `bridge.batch=false`（`main.go:1580` 零值构造，无人恢复），此后每次 plan(start) 都走 else 分支 `SetPreApproved(false)`（`main.go:410-412`）。**「re-arm via plan(start)」的契约在 resume 后永久失效**，用户批过的 auto-approve 静默降级为逐次 y/N；唯一的恢复路径是重新 propose 再批一遍。

**为什么这是「读全信息」而不是「建机制」**：marker 后缀本来就是 wire format 约定的一部分（AGENTS.md：「additions go AFTER the closing token」），信息已经在转录里，只是 reconstruct 没读。修复 = reconstruct 返回 batch 位 + resume 时 `bridge.batch = restored`（可选：若 currentIdx≥0 且转录尾部无 plan(complete/skip)，同时武装 preApproved）。恢复 batch 与「grant 不越上下文存活」原则不冲突——上下文（同一个 plan 的同一个 step，currentIdx 已恢复）没有变。

**若决定不恢复**：也应显式化——否则 marker 教给模型的 re-arm 契约与实际行为不一致。两案取一，本笔记推荐恢复。

**验收要点**（供实施者）：① `TestReconstruct` 加 batch 变体（marker 带后缀 → batch=true）；② resume 后 plan(start=N) 在 pref=Ask 下 write 免询问；③ plan(complete) 后恢复逐次询问；④ 不带后缀的旧会话行为不变。

### 6.3 todo：无需动作

seek 的清单需求已由 plan 子系统满足且已是 fold。若将来引入 dsh 式「模型自管 todo」（轻量、不经人类审批、turn 边界自动清空），照 plan 的形状做即可：todo 工具的 tool_call 参数就是整表，last-wins fold，reconstruct 扫最后一个 todo 调用——**零新机制、零缓存风险（append-only）、resume 一致性天然获得**。不需要为它预先建任何基础设施；它落在 §5 判据的「免费」一侧。

### 6.4 与既有结论的关系

- **维持 `dsh-analysis.md` §9.2 对「approval 事件溯源化」的否决**，并把论据从「前提不成立」（当时的理由：pref 单 bit 已持久化）升级为「前提不成立 + 事件源缺失 + 成本三重」——permission-refactor 引入 alwaysAllow/preApproved 后原论据已不完整，本笔记重新推导后结论不变。
- **精化 §9.4 的嗅探测试**：「给 struct 字段同时写 reconstruct() = 两个真相源」依然对，但要补一句——有些状态（TUI 触发的）**本就不可 fold**，对它们正确的形态是「运行时字段 + 事件点单点写 + 转录边界只恢复可推导部分」，seek 的 preApproved 实际上已是这个形态，防御性重置链是该形态的内生成本而非可消除的债。
- **§9.4 的纪律原文不动**：本笔记是它在 approval/todo 两个子系统上的专项展开，不是修订。

---

## 附：证据锚点

- `internal/permission/permission.go` — 196-235（状态字段与 alwaysAllow 设计注释）、587-677（Check 分辨顺序）、388-414（SetAlwaysAllow「never persisted」）
- `internal/tools/plan/reconstruct.go` — 43-125（只 fold steps/currentIdx，丢弃 marker 后缀）
- `internal/tools/propose/propose.go` — 338-346（marker 生成 + re-arm 契约文案）
- `cmd/seek/main.go` — 903-934（resume 继承 yolo/plan bool）、110-148（planBridge 生命周期注释）、1580-1624（resume 重建：Restore steps、batch 不恢复）、1960-2016（SetYolo/AlwaysAllowKind/SetPlan/SetPlanSubstate 接线）
- `internal/tui/update_agent.go` — 759-780（persistSession：回写 Yolo、不回写 Plan）
- `docs/prd/feature-permission-refactor.md` — §3.3（SetWorkflow 重置 preApproved）、§十一（alwaysAllow 全生命周期：「从不持久化（resume 后重新询问）」）
- `docs/dsh-analysis.md` — §2（dsh fold 全景）、§8.2（缓存纪律对比）、§9.2（原否决）、§9.4（纪律与边界）
