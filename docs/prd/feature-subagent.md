# Feature: 子代理 + Worktree 隔离

**所属版本**：v5（柱 G · 空间维度编排）
**前置阅读**：[PRD v5 umbrella](v5.md) §2（v5 特有约束六条）、[PRD v0](v0.md) §4.7（Permission）、§4.8（Tool）、[feature-permission-refactor.md](feature-permission-refactor.md)（R1+R1.1 双轴模型）
**状态**：🚀 已交付（M11.0 + M11.1 全部落地，v0.6.0）
**目标里程碑**：M11.0（子代理核心）+ M11.1（worktree 集成）
**目标发版**：v0.6.0

---

## 1. 动机

seek 当前是单 agent 单进程——模型在一条会话里串行干所有事。这有两个体感缺失：

- **并行研究的不可表达**——用户问"同时帮我看看 `internal/tui`、`internal/permission`、`internal/checkpoint` 这三个包谁负责处理 Esc"，模型只能挨个 grep，turn 数和上下文都线性增长。三个独立的探索本可以并行，但没有 spawn 机制。
- **长上下文保护的不可表达**——子任务"读 50 个文件归纳风格"会让父 transcript 膨胀到失去 prefix-cache 复用价值；而真正需要回流的只是结论。Claude Code 的 `Agent` 工具允许"让子上下文吞掉这 50 个文件，只把 300 字总结塞回父"。
- **角色专精的不可表达**——code-reviewer 的 system prompt 应当跟 general agent 不同；plan-only explorer 应当永远在 `WorkflowPlanAnalyze` 里。当前 seek 让用户在主会话里反复切 mode 或换 skill，状态污染严重。

本 PRD 一次性补齐：

- **`agent` 工具**——模型可 spawn 一个子 agent，由独立的 session JSONL / Tracker 承载，父只收一条 summary。
- **三个内置 `subagent_type`**——`general-purpose` / `explore` / `plan`，覆盖最常见的三种用例。
- **Worktree 隔离（M11.1）**——`enter_worktree` / `exit_worktree` 工具，且 `agent` 的 `isolation: "worktree"` 自动联动；并行实施时各 subagent 不互踩文件。

## 2. 设计目标与不做什么

### 目标

1. **父字节流确定性**——`agent` 工具的 schema 与 wire-format result 都是字节级稳定（v5 §2.1）；prefix cache 不受 spawn 影响。
2. **权限单调收紧**——子 Policy 严格 ≤ 父 Policy（v5 §2.3）；LLM schema 不暴露任何能扩权的参数。
3. **成本透明归集**——子 token 滚入父 Tracker，状态栏数字 = 父 + 全部活跃/已完成子。
4. **失败降级**——子 spawn 失败 / worktree 不可用 / 子超时 → 父收到结构化错误 tool result，不崩溃。
5. **可独立 resume**——子 session JSONL 走标准 `internal/session` 格式，`seek -resume <sub-sid>` 能续传一个子（v5 §2.2）。

### 不做什么（v5 明确延后）

- ❌ **嵌套 spawn 深度 > 1**——子不能再 spawn 孙。v5 限定深度 1，避免 fork-bomb 与责任归属混乱；如果实战反馈强烈，v6 再开。
- ❌ **子 → 父消息回灌**——子结束只回 summary string；不允许子在执行中"对父说话"。
- ❌ **`agent.bypass_hooks`**——子默认走父 `hooks.Registry`（v5 §3.6）；不开 bypass 开关。
- ❌ **`subagent_type` 注册表开放给 skill**——v5 三类型硬编码；让社区贡献专精 agent 是 v6 候选。
- ❌ **跨 worktree 自动合并**——`exit_worktree` 只清理 / 报告分支，不替模型 cherry-pick。
- ❌ **子 agent 实时流式回显到父 TUI**——v5 父 TUI 只显示"agent 进行中…12s"占位；live streaming 留到后续版本。
- ❌ **`-p` / 非交互模式下的 spawn**——单文件 print 模式不启用 `agent` 工具（无 TUI 渲染面板，没必要）。

## 3. 数据模型与签名

### 3.1 `agent` 工具的 LLM schema（package-level 常量字节）

```json
{
  "type": "object",
  "properties": {
    "description": {
      "type": "string",
      "description": "Short (3-5 word) description of the task. Shown in /agents UI."
    },
    "prompt": {
      "type": "string",
      "description": "The full task for the subagent. The subagent sees nothing else from the parent — write self-contained briefing including any context the subagent needs."
    },
    "subagent_type": {
      "type": "string",
      "enum": ["general-purpose", "explore", "plan"],
      "description": "general-purpose: full tools, parent's model. explore: read-only tools (read/grep/list_dir/git/webfetch/think), good for parallel research. plan: forces plan-analyze workflow."
    },
    "isolation": {
      "type": "string",
      "enum": ["none", "worktree"],
      "description": "worktree creates a temporary git worktree so the subagent works on an isolated copy. Auto-cleaned if no changes; path+branch returned if changes."
    }
  },
  "required": ["description", "prompt"],
  "additionalProperties": false
}
```

`subagent_type` 缺省 = `general-purpose`；`isolation` 缺省 = `none`。**不**暴露 `model` 覆盖（v5 简化决策；用户想换模型走父级 `/model`）。

**长度约束在 Execute 内强制**——JSON Schema 的 `maxLength` 在 seek 的 `tools.UnmarshalStrict` 链路上**不被强制**（它只做 `DisallowUnknownFields`）。`agent.Execute` 在解析后显式校验：`description` > 120 字符 → 截断并加 hint；`prompt` > 32 KB → 返回 `[agent: failed reason=prompt_too_long]`。schema 里的 description 是给模型看的软提示，硬约束在 Go 代码里。

### 3.2 Wire-format 父端 tool result（v5 §2.1）

成功：

```
[agent: completed] <one-line headline derived from subagent's final assistant message>

<full summary body — what the subagent returns via its final assistant message, capped at MaxSummaryBytes>

— sub-sid: <8-char short id> · turns: <n> · tokens: <prompt+completion>
```

失败 / 取消 / 超时：

```
[agent: failed reason=<canceled|timeout|spawn_error|hooks_denied|max_turns_exceeded|prompt_too_long|too_many_subagents>] <short hint>

— sub-sid: <8-char short id>
```

**字节稳定性契约**——只**前缀**是契约：

- `[agent: completed]` 与 `[agent: failed reason=<...>]` 是被反向解析的前缀（与 `[plan: approved]` 同等级），扩展项加在闭合 `]` 之后，永不内嵌新关键字。
- 真正的契约**到 headline 行 + 一个换行**为止。`reason=` 集合是封闭枚举，新增 reason 算 minor schema 变更，需带回归测试。

**Headline 之后（summary body + footer）属于 display 区**，不是字节稳定契约——summary 由模型生成天然不稳定，footer 里的 `turns: ` / `tokens: ` 数字本就因 turn 而异。**Footer 故意不含 cost**——`pricing.FormatCost` 格式可能跟随定价表演进而漂移，把它放进 tool result 会让 prefix cache 在定价表更新时被无意义击穿；cost 走状态栏从 `Tracker.CumulativeCost` 直读。

**`MaxSummaryBytes` 常量**——`internal/subagent/wireformat.go` 包级 `const MaxSummaryBytes = 4096`（与 grep 工具的 byte cap 同源量级；用单元测试守门，模型生成超长结尾时截断 + 追加 `\n…(truncated)`）。

### 3.3 子 session 目录布局

每个子 session 拥有一个**目录**而非孤立文件，承载 transcript + 衍生 artifacts：

```
~/.seek/projects/<project-id>/sessions/<sid>/subagents/<sub-sid>/
├── transcript.jsonl     # 子 session 转录（标准 internal/session 格式，schema_version=2）
├── plans/               # 子用 propose 工具时的 plan artifact（v2 plan-mode 同结构）
└── checkpoints/         # isolation:"worktree" 时子的文件 checkpoint blob/index（v3 checkpoint 同结构）
```

`plans/` 与 `checkpoints/` 按需创建——纯研究型 `explore` 子不会产生任何子目录，只有 `transcript.jsonl`。

- `<sub-sid>` 是**独立**短 ID（与父 `<sid>` 同形态，但完全独立空间），便于 `seek -resume <sub-sid>` 直接续。`-resume` 时通过子 transcript header 找父 sid + project id 做 cwd 等上下文恢复。
- `<sid>` 是父 session 短 ID。
- 子 JSONL 走标准 `internal/session` 包格式（schema_version=2，line-1 header，N-1 行 message）。

**为什么按目录而非纯文件**：v5 §3.4 父-子索引、§5.4 子 plan artifact、§5.2 子 checkpoint 都需要在子粒度下分别落盘；目录式布局让 `seek subagent kill / resume / list` 的实现可以"取一个子 = 操作一个目录"，避免散落在三处的路径拼接。

### 3.4 父-子关系索引（事件源风格）

```
~/.seek/projects/<project-id>/subagents.jsonl
```

**Append-only 事件源**——每行一条事件，不是一条 "记录"。状态由后置事件覆盖前置事件得出，与 plan-mode `reconstruct.go` 同样的事件溯源风格。理由：jsonl 不可原地更新；若每子只写一行最终态，子还活着时面板无东西可渲染。

事件类型：

```json
{"event": "started",   "sub_sid": "20260601-103412-7a3f", "parent_sid": "...", "parent_turn": 4, "ts": "...", "type": "explore", "description": "find Esc handlers", "worktree_path": null}
{"event": "completed", "sub_sid": "20260601-103412-7a3f", "ts": "...", "tokens": {"prompt": 8421, "completion": 612, "cache_hit": 8100}}
{"event": "failed",    "sub_sid": "20260601-103412-7a3f", "ts": "...", "reason": "canceled"}
{"event": "killed",    "sub_sid": "20260601-103412-7a3f", "ts": "..."}
{"event": "orphaned",  "sub_sid": "20260601-103412-7a3f", "ts": "..."}  // resume 扫描发现：有 started 无终态
```

**状态折叠规则**：按 `sub_sid` 分组，取**最后一条**事件为当前状态。无终态事件 ⇒ `active`。

`completed` / `failed` / `killed` / `orphaned` 是终态；写入后该 sub_sid 不再产生新事件。

用途：`seek subagent list` 读这一份并折叠；`/agents` TUI 渲染折叠后的状态；`seek` 启动时扫一遍，对每个**只有 started** 的 sub_sid 立刻追写一条 `orphaned`（处理上次崩溃的活跃子）。**不**进 session JSONL（v5 §2.6）。

**Cost 不入索引**——cost 由父 `Tracker.CumulativeCost` 即时计算，避免子完成时 token-cost 转换的精度与定价表版本耦合进持久态。

### 3.5 `permission.Policy.Spawn` 签名

```go
// Restriction tightens a Policy when spawning a subagent. Each field is
// monotonic-only: values can move toward MORE restrictive (e.g.
// PrefYolo → PrefAsk) but never less. Nil pointer = inherit from parent.
type Restriction struct {
    Pref     *Preference // nil = inherit parent's pref
    Workflow *Workflow   // nil = inherit
}

// Spawn returns a new Policy for a subagent rooted at the given cwd.
// cwd is REQUIRED — subagents under isolation:"worktree" run at the
// worktree path, not the parent's cwd, so it cannot be inherited
// implicitly. For isolation:"none" the caller passes parent.Cwd()
// explicitly. (Policy.cwd is set at construction and never changes
// post-hoc; that invariant is preserved by always taking it on Spawn.)
//
// Enforces (returns error if Restriction would loosen any axis):
//   - PrefDeny  → only PrefDeny
//   - PrefAsk   → PrefDeny or PrefAsk (never PrefYolo)
//   - PrefYolo  → any pref
//   - WorkflowPlanAnalyze (parent) → child MUST also be PlanAnalyze
//   - WorkflowPlanExecute (parent) → child PlanExecute or PlanAnalyze
//   - WorkflowNone        (parent) → child any
//
// preApproved is NEVER inherited — even when child stays in
// PlanExecute, the per-step batch-approval gate must be re-established
// inside the child's own plan flow. This prevents a parent step's
// auto-approve window from silently extending into a fresh subagent.
//
// Never returns a Policy looser than the receiver.
func (p *Policy) Spawn(cwd string, r Restriction) (*Policy, error)

// Cwd returns the policy's working directory (set at construction).
// Added in v5 to support Spawn — read-only accessor, no setter.
func (p *Policy) Cwd() string
```

子 Policy 拥有**独立**的 `askFn`（typically wired to the same TUI with a "[subagent <sub-sid>]" prefix）和**独立**的 `onDestructive`（指向子 checkpoint manager；详见 §5.4）。父子 Policy 之间无 mutex 共享——一个用户操作要么影响父要么影响子，不串。

### 3.6 三个 `subagent_type` 模板

| type | Tools 子集 | SystemPrompt | 默认 Restriction |
|---|---|---|---|
| `general-purpose` | 父 Registry 全集 **减** `agent` / `ask_user` | role 提示 + 摘要长度提示（见下） | inherit |
| `explore` | read / grep / list_dir / git / webfetch / think | role 提示 + "You are in research-only mode. You cannot write, edit, or run mutating commands. Return findings as bulleted summary." + 摘要长度提示 | Workflow=`PlanAnalyze`（强制只读，即使父 Yolo） |
| `plan` | read / grep / list_dir / git / webfetch / think（与 explore 同） | role 提示 + "You are in plan-analyze mode. Investigate the task and return a numbered, structured plan in your final summary — explicit steps the parent (or a human reviewer) can execute. You cannot run mutating tools yourself." + 摘要长度提示 | Workflow=`PlanAnalyze` |

**为什么 `plan` 与 `explore` 共用同一只读子集（C.2 决策，M11.0）**：原 v1 设计把 plan 定为"父全集 + 用 `propose` 工具"，但实现时发现两个硬约束让该路径走不通——

1. `propose` 工具的 sink 绑定父 session（`plan_bridge` 捕获父 `activeSession` + plan panel）。子调用 `propose` 会把 artifact 写到父 plan 目录、事件灌到父 plan panel，形成跨上下文污染。
2. M11.0 production wiring 复用父 reg 的 Tool 实例（共享父 Policy）。子的 `Workflow=PlanAnalyze` 限制**不被** tools 自己的 `Check()` 强制（PRD §2.3 monotonic-收紧 promise 在这一路径上只能软兜底于 system prompt 指令）。

**净效果**：`plan` 与 `explore` 在工具能力上**完全相同**——区别只在 Extra clause 的 framing。`explore` 输出 bulleted findings；`plan` 输出 numbered structured steps。父读到子 summary 后，如有执行需要，由父在主上下文里调 `propose`。这是 M11.0 的 ship-with 决策；未来若 per-spawn Registry 重建（PRD §8 风险表 "Policy passthrough"）落地，可重新评估让 `plan` 拥有 `propose` 工具的可行性。

**统一的 role 提示**（首行，三模板共用）：

> "You are a subagent spawned by the parent agent for: `<description>`. Complete the task and return a concise summary as your final message. Do not engage in conversation."

**统一的摘要长度提示**（末行，三模板共用）：

> "Your final assistant message will be returned to the parent as a summary. Keep it within ~4000 characters; content past that will be truncated."

理由：让模型自己控制长度比事后截断好得多——避免父收到半截 sentence。`~4000` 是 `MaxSummaryBytes = 4096` 的近似宽度（§3.2），口径松一点让模型有缓冲。

模板由 `internal/subagent/types.go` 维护，从父 Registry 派生子 Registry 时做白名单过滤（不复用整个 `tools.Registry` 实例——子可能少几个工具）。

**`ask_user` 在所有子模板中默认不注册**——子调用 `ask_user` 会落到与父共享的 TUI picker 上，用户难以分辨"是父在问还是子在问"，UX 上容易误操作。v5 选择保守：子需要决策时只能在 prompt 里编码足够上下文，或用 `[agent: failed reason=needs_user_input]` 退回父让父代为提问。等 v6 真有 disambiguation UI（"[subagent X asks:]" 前缀的 picker 样式）再开放。

**`agent` 在所有子模板中**也**默认不注册**——v5 限定 spawn 深度 = 1（§2 anti-goal）。这是 §6 验收 #13 "嵌套防护"的第一道防线，配合 Execute 守卫做防御深度。

### 3.6.1 子 system prompt 的完整组成

子 system prompt **不是**单一字符串，是**与父同源 + 模板覆盖**的拼装结果。组成顺序（从上到下）：

| 段 | 内容 | 子是否继承 | 理由 |
|---|---|---|---|
| 1. AGENTS.md / CLAUDE.md（项目约定） | 项目约定（pitfall 录、edit→read first、permission model 等） | ✅ **完整继承** | 项目级约定对子同样有效；子也工作在同一项目内，违反就是 bug |
| 2. M-index（项目记忆） | 项目历史决策、长期 pitfall 索引 | ✅ **完整继承** | 设计决策对子同样有价值（如 "session 持久化必须 Sync 再 Close"），否则子可能给出与项目实践冲突的建议 |
| 3. Skill manifest | 已安装 skill 的入口段 | ✅ **完整继承** | 工具可用性应当一致——父能用 `Skill` 调 ui-ux-pro-max，子也应能 |
| 4. Permission mode reminder | 父的 mode 标签（`yolo` / `plan-analyze` / `plan-execute`） | ❌ **不继承** | 由 §3.6 template 的角色提示**替代**——子的 mode 由 `Policy.Spawn` 决定，不应在 system prompt 末尾追加可能与 template 冲突的提醒 |
| 5. 子 template 角色提示 + 摘要长度提示 | §3.6 表格里 SystemPrompt 列 | ✅ **追加** | 子特有；标识"你是 subagent"的关键信号 |

实现：`internal/subagent` 在构造子 `pkg/agent.Config.SystemPrompt` 时复用 `cmd/seek` 的 system prompt 装配函数，仅在末尾**替换**第 4 段（mode reminder）为第 5 段（template prompt）。新装配函数应当抽到 `internal/sysprompt`（或类似），避免 cmd/seek 与 internal/subagent 双份代码漂移。

**派生约束**：父 system prompt 的字节构造必须 **deterministic**（前缀 cache 依赖）；子继承前 3 段时同样要求字节相同——这意味着即使父 mode reminder 改变，前 3 段的字节也不变。当前 `cmd/seek` 的拼装已经满足，子复用即可。

### 3.7 成本归集：`cache.Tracker.AdoptChild`

```go
// AdoptChild registers a subagent's Tracker so its Record() calls roll
// into THIS Tracker's Cumulative() / CumulativeCost() output. The child
// Tracker continues to receive its own Record() calls independently;
// AdoptChild only changes the parent's aggregation.
//
// Multiple children (parallel subagents) are supported and aggregated
// additively. Calling AdoptChild on an already-adopted child is a
// no-op. Concurrency: safe under Tracker.mu.
func (t *Tracker) AdoptChild(child *Tracker)
```

实现：`Tracker` 新增 `children []*Tracker` 字段；`Cumulative()` / `CumulativeCost()` 在读取自身 turns 后追加遍历 `children`。子 Tracker 自身 API 不变，可独立 `Summary()`、独立持久化到子 session header。

**派生**：状态栏底栏的 `cumulativeCost` 自动包含子；不需要 TUI 改动。

### 3.8 Worktree（M11.1）

**`enter_worktree` 工具**：

```json
{
  "type": "object",
  "properties": {
    "branch": {"type": "string", "description": "Branch name to create or use. Auto-generated if omitted."},
    "base": {"type": "string", "description": "Base commit/branch (default: current HEAD)."}
  },
  "additionalProperties": false
}
```

返回：`[worktree: created path=<abspath> branch=<name>]`

实现：

```
git worktree add <~/.seek/projects/<pid>/worktrees/<sub-sid>> -b <branch> <base>
```

git ref 命名空间 `refs/seek/worktrees/<sub-sid>` 不在 default refspec（与 v3 checkpoint 同源）。

**`exit_worktree` 工具**：

```json
{
  "type": "object",
  "properties": {
    "path": {"type": "string"},
    "if_dirty": {"type": "string", "enum": ["keep", "discard"], "description": "keep (default): leave the worktree and branch in place, return path+branch. discard: hard-reset and remove."}
  },
  "required": ["path"],
  "additionalProperties": false
}
```

返回：
- clean → `[worktree: cleaned]`
- dirty + keep → `[worktree: kept path=<...> branch=<...> changes=<n>]`
- dirty + discard → `[worktree: discarded changes=<n>]`（先把 dirty 内容 `git stash create` 到 `refs/seek/discarded/<ts>` 救命引用，再 hard-reset 删除）

**`refs/seek/discarded/<ts>` 救命栈的 GC**：与 v3 checkpoint 同样**不引入常驻 daemon**（v5 §2.5）。两条 GC 通道：
- **`seek` 启动时被动 GC**——扫 `refs/seek/discarded/`，删 timestamp 早于 48h 的 ref。代价低且与 v3 git ref GC 同栈。
- **显式 `seek worktree gc`**——立即清空所有 discarded refs，文档建议用户磁盘紧张时手动调用。

48h 窗口的依据：与人类"反应过来想恢复"的典型时窗一致，再长就需要更结构化的 trash 系统（v6 候选）。

**`agent` 的 `isolation: "worktree"`**：子 spawn 前自动 `enter_worktree`，子结束后自动 `exit_worktree` (`if_dirty: keep`)；子的 cwd 设为新 worktree 路径。父收到的 tool result 在标准 summary 后追加：

```
— worktree: <abspath> (branch <name>, <n> changes)
```

无改动时不追加（自动 cleaned）。

## 4. CLI 与 TUI 命令

### 4.1 CLI

```
seek subagent list [--parent <sid>] [--status active|completed|failed|all] [--json]
    列子 session（默认当前项目所有；--parent 限定父）

seek subagent resume <sub-sid>
    把一个子 session **提升**为新的顶层会话继续交互：
    - 重写 system prompt 为标准 root system prompt（去掉 "You are a subagent…" 的角色提示）
    - 父-子关系索引追写一行 `event: "promoted"` 闭合状态（避免父端 /agents 面板继续显示"活跃"）
    - 启动后等价于一次普通的 -resume，不再受父 Policy 约束（用户接管）
    适用场景：子查得不错但摘要不够，想直接接着这个子的上下文人工继续。

seek subagent kill <sub-sid>
    通过 ctx cancel 终止活跃子；clean shutdown 写入 transcript final marker

seek -list --include-subagents
    -list 默认不展开子；显式 flag 才合并展示
```

### 4.2 TUI slash 命令

```
/agents
    打开面板列出当前 session 已 spawn 的子：sub-sid · type · status · 描述 · 累计 token · 累计 cost
    回车一个条目 → 详情视图（最末 summary 预览）
    `k` 键 → kill 该子（带确认）

/worktrees
    列当前项目下 seek 管理的 worktree（路径、branch、关联 sub-sid、change count）
    （仅在仓库是 git working tree 时显示）
```

### 4.3 状态栏反馈

- spawn 子 → 状态栏右侧出现 `⤴ N agents` 计数器（N = 活跃子数），子完成减一
- 子完成时短暂闪 `✓ agent done` 2 秒
- 累计 cost 数字自动包含子（来自 `Tracker.CumulativeCost`）

## 5. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/tools` | 新工具 `agent` / `enter_worktree` / `exit_worktree` | 中（三个新工具包） |
| `internal/permission` | `Policy.Spawn(Restriction)` 方法 + `Restriction` 类型 + 测试 | 小（一个方法） |
| `internal/cache` | `Tracker.AdoptChild` + 修改 `Cumulative` / `CumulativeCost` 遍历 children | 小 |
| `internal/subagent`（新包） | 模板表（types.go）+ 子 runner 工厂 + 父子关系索引 jsonl 维护 + spawn/list/kill API | 中-大（柱 G 核心） |
| `internal/worktree`（新包） | git worktree 包装：create / status / cleanup | 中（M11.1） |
| `pkg/agent` | `Config` 增加 `IsSubagent bool` + 子 runner 用同一 Agent 但传不同 Tools/Policy/Tracker | 极小（只加标识位） |
| `internal/paths` | 新增 `SubagentSessionPath(pid, sid, subSid)`、`SubagentsIndex(pid)`、`WorktreesDir(pid)` | 小 |
| `internal/session` | 子 session 不进 `List()` 默认结果；接受 `IncludeSubagents` 选项 | 小 |
| `internal/tui/commands.go` | 注册 `/agents` `/worktrees` | 小 |
| `internal/tui/`（新文件 `agentspanel.go`） | `/agents` 面板渲染 | 中 |
| `cmd/seek` | 新 `subagent` 子命令；`-list --include-subagents` flag | 中 |
| `internal/hooks` | 子 runner 在构造时直接复用父 `*Registry`——无需新 hook 类型 | 0 |
| `pkg/deepseek` | **不变** | 0 |

### 5.1 Prefix cache 影响审计（细化）

- `agent` 工具的 schema 字节是 package 常量 → 父 Registry `Wire()` 输出确定性 → cache key 不变
- 父收到的 `[agent: completed] ...` tool result 字节由 summary 决定；这与现有 `bash` / `webfetch` 工具的 stdout result 同形（用户/模型动作的副作用）
- 子 streaming bytes 完全不进父 messages[] → 零扰动

### 5.2 与 v3 checkpoint（柱 A）的联动

- **`isolation: "none"`**（默认）：子在父 cwd 跑；如果触发破坏性操作，复用父的 `onDestructive` → 父 turn 仍有 checkpoint。
- **`isolation: "worktree"`**：子拥有**独立** checkpoint manager（root 在新 worktree 路径）；这避免子的 git stash 操作污染父仓库。父在 spawn worktree subagent 之前**也**打一次 checkpoint（"准备 spawn worktree"），保障 worktree 创建失败时父无残留。

### 5.3 与 v3 hooks（柱 B）的联动

子工具调用走**父的** `hooks.Registry`：

- `pre_tool` deny rule 对子同样生效（如父项目禁止 `rm -rf`，子也禁）
- `post_tool` observer 对子同样触发（audit log 记录子的命令）
- audit log 行加 `subagent_sid` 字段以便区分

理由：用户的 `hooks.toml` 是项目级安全策略，绕过 = 越权。

**Worktree 路径映射**——`isolation: "worktree"` 时子的 cwd 是 `~/.seek/projects/<pid>/worktrees/<sub-sid>/`，但父 `hooks.toml` 写的规则（如 `path_glob = "docs/prd/**"`）是基于项目根的相对路径。**hook 匹配前必须把子的绝对路径映射回项目根的等价路径**：

```
worktree_root + relative_path   ⟶   project_root + relative_path
```

实现：`hooks` Registry 在收到 `PreToolUseHook` 的 path 字段时，若 path 落在已知 worktree 路径下，先用 `worktree.MapToProject(path)` 改写成项目根下的等价路径，再做 glob 匹配。映射表由 `enter_worktree` 注册、`exit_worktree` 注销。

为什么这样：worktree 是**隔离写文件冲突**的，**不是**绕过项目安全策略的。如果父项目禁止改 `docs/prd/`，子在 worktree 里改 `docs/prd/` 同样应被拒——hooks 是项目级策略，worktree 不应成为绕过手段。这与 §8 风险表"worktree 不是沙箱安全"是一致的：worktree 防工程性误碰，hooks 防策略性违规，两者职责分离。

audit log 在 worktree 场景下记录**两个**path 字段：`path_worktree`（实际操作的绝对路径）+ `path_project`（映射回项目根的相对路径）。审计阅读者直觉看 `path_project`，调试看 `path_worktree`。

### 5.4 与 v2 plan-mode 的联动

- 父在 `WorkflowPlanAnalyze` 时 `Spawn` 子 → 子强制 `PlanAnalyze`（v5 §2.3）
- 父在 `WorkflowPlanExecute` 时 `Spawn` 子 → 子默认 `PlanExecute` 继承，但子可显式声明 `PlanAnalyze`；且**子的 `preApproved` 一律为 false**（§3.5 Spawn 规则），父的 auto-approve 窗口不传给子
- 子 `propose` 工具调用**不**回灌父——子 plan artifact 落到子 session 目录下 `~/.seek/projects/<pid>/sessions/<sid>/subagents/<sub-sid>/plans/`（与 §3.3 目录布局一致），父 TUI 不渲染（避免父 plan panel 与子 plan panel 串台）

**子 `askFn` 路由规则**——子需要确认时（`Pref=Ask` 或 `PlanExecute` 下的破坏性操作）的 picker 弹给**父 TUI**，但视觉上**必须区分**：

- picker 标题前缀加 `[subagent: <description>]`，例如 `[subagent: find Esc handlers] Allow bash: rm -rf /tmp/foo?`
- picker 边框配色与父自身 picker 不同（建议用次要色调，如父用 highlight 蓝、子用 dimmed 紫）
- 若已有父 askFn 弹窗在排队，子 askFn 入队并按 FIFO 串行展示（不强占焦点）

实现：子的 `Policy.askFn` 是父 askFn 的**包装器**——`func(a Action) bool { return parent.askFn(decorate(a, subSid, description)) }`。父 TUI 端的 ApprovalRequest 渲染层根据 `Action.Display` 新增字段 `SubagentSid` / `SubagentDescription`（按 R1.1 "TUI 元数据放 Display 子结构"约定）判断是否走 subagent 样式分支。

为什么这样：子 askFn 走父 TUI 是必然的——子没有独立 TUI 进程。问题不在路由，在 disambiguation。视觉区分让用户在"父在问"vs"子在问"瞬间分辨，避免拿父的语境答了子的问题。

### 5.5 与 v4 suggested-reply（柱 D）的联动

子 runner 构造 `pkg/agent.Config` 时**不注入** `suggester.InjectCalibration` 的 `PrepareMessages` 钩子——suggester 是通过 `Config.PrepareMessages` 接入的（非独立 bool 开关），不挂钩子 = 自动关闭。理由：预测语境是真人用户在场，子没有。

派生：v5 §3.5 那条"`Config.SuggestReply = false`"在 v5 umbrella 中写得不准确——实际机制是"不注入 PrepareMessages"。提请 umbrella 微调（已同步）。

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | `agent` 工具 schema 在多次启动间字节完全一致 | 单元测试（hash schema 字节） |
| 2 | 父调用 `agent` 后，父 transcript messages 中**只**多一条 tool result，且符合 `[agent: completed] …` 前缀格式 | 集成测试（mock LLM） |
| 3 | 子 session JSONL 写到 `subagents/<sub-sid>.jsonl`，可独立 `seek -resume` | 集成测试（spawn → kill → resume） |
| 4 | `Policy.Spawn` 拒绝放宽：父 PrefAsk 不可生 PrefYolo；父 PlanAnalyze 必生 PlanAnalyze | 单元测试（表驱动） |
| 5 | 子全部完成后，父 `Tracker.CumulativeCost()` == 父自身 + 全部子之 `CumulativeCost()` 之和（活跃子在跑时不做 exact 断言，只断言**包含**关系） | 单元测试（mock 子完成后断言） |
| 6 | 并发 spawn 三个子，父端 tool result 三条相互独立，无 message interleave | 集成测试 + `-race` |
| 7 | 子 ctx 取消时 → 父收到 `[agent: failed reason=canceled] …` 且子 transcript 最后一行 marker 完整 | 集成测试 |
| 8 | `isolation: "worktree"` 自动创建 worktree、子结束后无改动则自动 cleanup | 集成测试（真 git） |
| 9 | 子触发破坏性操作时父 `hooks.Registry.PreToolUseHook` 链照常被调用 | 单元测试 |
| 10 | 非 git 仓库时 `isolation: "worktree"` 返回结构化错误，父收到 `[agent: failed reason=spawn_error] ...` | 集成测试 |
| 11 | `seek -list` 默认不列子；`--include-subagents` 列时子缩进显示在 parent 下 | 集成测试 |
| 12 | `/agents` 面板渲染期间 spawn 新子，面板自动刷新（不需 reopen） | 手测 + teatest |
| 13 | 嵌套 spawn（子内部又 spawn）被 `agent` 工具的 Execute 守卫拒绝 | 单元测试（IsSubagent=true → agent 工具不注册） |
| 14 | `go test -race ./...` 全绿（特别是 `internal/subagent` 并发表） | CI |
| 15 | 现有 v0–v4 测试套件零回归 | 现有测试 |

## 7. 实现计划

| 子 ms | 内容 | 估时 |
|---|---|---|
| **M11.0** | `internal/subagent` 包骨架 + `Policy.Spawn` + `Tracker.AdoptChild` + `agent` 工具 + 三个类型模板 + 父子索引 jsonl + `/agents` 面板 + `seek subagent` CLI | ~5 天 |
| **M11.1** | `internal/worktree` 包 + `enter_worktree` / `exit_worktree` 工具 + `agent.isolation` 联动 + 自动清理 + 与 v3 checkpoint 双层联动 + `/worktrees` 面板 | ~3 天 |

**发版策略**：M11.0 + M11.1 合并为 **v0.6.0**——两层一起 ship，避免用户疑问"为什么有子 agent 但不能并行实施"。worktree 是子 agent 真正成为"实施工具"而非"研究工具"的关键。

**实现顺序硬约束**：

1. M11.0 先做 `Policy.Spawn` 和 `Tracker.AdoptChild`（无 LLM 介入即可单测）
2. 再做 `internal/subagent` runner 工厂（包好 pkg/agent 复用路径）
3. 然后做 `agent` 工具（schema 冻结、Execute 委托给 runner）
4. **最小 `/agents` 列表面板**在步骤 3 之后立刻上——只渲染 sub-sid/type/status 三列，无交互；调试 spawn 路径需要它能看到子状态。完整面板交互（`k` 终止 / 详情视图 / 状态栏徽标）作为 M11.0 的收尾步骤 5
5. 完善 TUI 面板交互 + `seek subagent` CLI
6. M11.1 在 M11.0 验收后开工——worktree 工具需要子 runner 已稳定才能联调 `isolation` 路径

**并行可行性**：M11.0 与 M11.1 强串行；同一人推。M11.0 内部 `internal/subagent` 与 `internal/cache.AdoptChild` 可并行，但建议同人完成以保持 Tracker 接口一致性。

## 8. 风险

| 风险 | 缓解 |
|---|---|
| 用户误以为子能改父 transcript / 共享父记忆 → 反复抱怨"子查到的东西忘了"| `agent` 工具 description 显式写明 "subagent has zero memory of parent context except the `prompt` field"；book chapter 配图说明 |
| 并发 spawn N 个子 → N 倍 API rate 限流命中 | Config 加 `MaxConcurrentSubagents`（默认 3）；`agent` 工具在已有 N 个活跃子时**立刻返回** `[agent: failed reason=too_many_subagents] <n> active, retry after one completes` —— 模型按现有错误处理 pattern 自行决定 retry 或换策略，**不引入阻塞式工具调用**（与现有 `permission.ErrDenied` 走同样的"结构化失败 → 模型自决"路径） |
| 子无限循环（如 explore 子反复 grep 同一目录）→ 永不返回，父 turn 永不结束 | 子 runner 继承父 `MaxTurns`（默认 200）；用尽即 `[agent: failed reason=max_turns_exceeded]` |
| 子 transcript 损坏（断电、磁盘满）→ `Tracker.AdoptChild` 已注册但 child 永不 Record | spawn 失败时立即从 `children` 移除；spawn 成功后子 runner panic 路径写一行 `kind: "subagent_aborted"` 到父子索引 jsonl |
| Worktree 在 `enter_worktree` 之后 / `exit_worktree` 之前进程崩溃 → orphan worktree 残留 | `seek` 启动时**双向**扫描：(a) `~/.seek/projects/<pid>/worktrees/` 中有目录但 `git worktree list` 无 → seek 目录是空壳，提示 `seek worktree clean` 或手动 `rm -rf`；(b) `git worktree list` 有但 seek 索引里没 → 用户手动 `git worktree add` 的，seek 不管，但 `/worktrees` 面板标 "external" 让用户知道 seek 没在管。两类均**不自动清理**——可能是用户保留的或手动管理的 |
| 用户在 dirty worktree 里 `exit_worktree if_dirty=discard` 但其实想 keep | discard 路径**先 `git stash create` 一次**到 `refs/seek/discarded/<ts>`（不在 default refspec）。GC 走 §3.8 双通道（启动时被动 + `seek worktree gc` 显式）；不依赖 git `gc --auto`，因为后者**不清 dangling refs**。意外恢复路径写入 release notes |
| 父 Policy `PrefYolo` + 子 type=`explore` → 用户直觉认为 explore 一定只读但实现错把 yolo 传给子 → 静默写文件 | `Policy.Spawn` 单测覆盖；`explore` 模板 Restriction 硬编码 `Workflow=PlanAnalyze`（即使父 yolo），独立断言 |
| `Cumulative()` 走 children 链时若 children 也有 children（理论上 v5 禁止但代码可能滑出）→ 重复计数 | `AdoptChild` 检测 `child.children` 非空时 panic + 测试覆盖；同时 `agent` 工具在 `IsSubagent==true` 的 Registry 里**不注册**——双重保险 |
| 子结束时 Tracker 同步 vs 父 Cumulative 读取的 race | `AdoptChild` / `Cumulative` / `Record` 全部走 `Tracker.mu`；race 测试覆盖 |
| 子在 PlanExecute 父下默认继承 PlanExecute，但子未经 propose-approve 就直接 write → 越权 | 子 Policy.Workflow=PlanExecute 但 `preApproved=false`（spawn 不传 preApproved）；子若要执行 destructive 仍需通过 askFn 或子自己跑一次 plan-mode |
| Worktree 隔离下子用 `bash` 跑 `echo poison > ../../parent/foo` 或 `rm -rf /abs/path/outside` 越界 | **v5 不做路径沙箱**——`bash.cmd.Dir` 的 pin 防的是 cwd 漂移，不是绝对路径越权。文档 + 工具 description 明确"`isolation: worktree` 防的是文件冲突与并行实施，**不是**沙箱安全；不要让不可信子代理代跑 untrusted 代码"。真正沙箱留到 v6（评估容器化或路径白名单） |
| `-resume` 父 session 时父 baseUsage 已含子的累计 → 再 `AdoptChild` 子 Tracker → 重复计数 | `AdoptChild` 不调用于已完成的子；resume 只读子的 `subagents.jsonl` 最终事件做 `/agents` 渲染。**Tracker 加 "adoption flag"** 字段，标记已被父 baseUsage 吸纳的子，再次 AdoptChild 时短路 |
| `-resume` 时遇到 `started` 无终态的子 → `/agents` 面板显示"3 个活跃"但实际进程都死了 | resume 启动钩子扫 `subagents.jsonl`，对所有 `started` 无终态的 sub_sid 立即追写一行 `event: "orphaned"`（§3.4 状态折叠规则自动处理）；同步 fire `SessionStartObserver` 让相关清理（如 orphan worktree 检测）一并触发 |
| **Policy passthrough**（M11.0 已知限制）：子 agent 调 write/edit/bash 时工具用的是**父 Policy** 做 `Check`，子的 `Workflow=PlanAnalyze` 限制**不被工具 Check 强制**。原因：M11.0 production wiring 复用父 reg 的 Tool 实例，这些工具在父 reg 构造时已绑死父 Policy 引用 | **M11.0 ship-with 三层保护**：(a) `explore` 子模板直接不包含 write/edit/bash —— hard safety via tool whitelist；(b) `plan` 子模板 §3.6 已窄化为只读子集（C.2 决策）—— 与 explore 同；(c) `general-purpose` 子继承父 Policy 是设计意图，passthrough 等价。**未决项**：未来若需让 plan 用 `propose` / 真正 hard-enforce 子 Workflow，需做 **per-spawn Registry 重建**（重构 6 个 Policy-持有 tool 的构造路径 + 子构造闭包），追踪到 v0.6.x dot release |

## 实现偏差 & 交付后修复

### G1 — 生产 Runner 写入子转录 JSONL（commit `d2fa0a9`）

**问题**：M11.0 production wiring 中 `Runner.run()` 没有把子 agent 的完整对话写入其独立 session JSONL，导致子 session 在 seek 异常退出后无法 `-resume`。

**修复**：在 `Runner` 闭包中注入 `session.Saver`，每次 LLM 交互后写入子转录。同时修复了一个关联问题：`session.Save()` 在子路径下未正确创建父目录。

**教训**：设计稿假设 production wiring 会自动包含 session 持久化——但 M11.0 的 MVP wiring 更关注可用性而非持久化，G1 才补上。

### G2 — Tracker.AdoptChild resumed parent 双重计数守卫（commit `8e30b1d`）

**问题**：resume 一个父 session 时，`Tracker.AdoptChild` 会因为 replay 所有子 adoption 事件而重复追加 child 引用，导致 `CumulativeCost()` 数字膨胀约 2×。

**修复**：在 `Tracker` 上增加 `adopted` 标记位——resume 路径反序列化后设置标记；`AdoptChild` 在标记为 true 时跳过追加。同时完善 `Tracker.Reset()` 以正确清除该标记。

**lesson**：事件溯源 resume 路径上，任何"追加"而非"设置"的操作都需要幂等守卫。设计稿的 resume 分析（§3.7）提到了 orphan 风险但漏掉了双重计数，G2 才补全。

## 9. 后续版本

- **v0.6.x dot**：子 agent 实时流式回显到父 TUI（可折叠区域）—— v5 只做 "agent in progress" 占位
- **v0.6.x dot**：`MaxConcurrentSubagents` 暴露为配置项 + `/agents` 面板内提升/降低
- **v0.6.x dot**：**per-spawn Registry 重建**——让 6 个吃 Policy 的工具（read/write/edit/bash/memorytool.Remember/skillinstall.Commit）在 Manager.Spawn 时用 childPolicy 重构造，PRD §2.3 monotonic-收紧 promise 在工具 Check 级别也 hard-enforce。这一项落地后即可放开 plan 模板的工具子集，让它真正持有 `propose`（须先解决 propose sink 跨上下文污染——可能要给 propose 一个 sub-aware bridge）
- **v6**：嵌套 spawn 深度 > 1（评估后决定）
- **v6**：`subagent_type` 注册表开放给 skill（社区分发 code-reviewer / security-reviewer 等专精 agent）
- **v6**：跨 worktree 合并辅助工具（`merge_worktree`）+ 三方合并冲突提示
- **v6**：子 → 父结构化数据回传（不只是 string summary，可返回 JSON）
