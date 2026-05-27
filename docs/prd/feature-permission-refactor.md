# Permission 重构 R1：Mode 拆成 Preference + Workflow 两轴

**主题**：当前 `permission.Mode` 同时编码两个正交的概念——**用户偏好**（我想多严，Deny / Ask / Yolo）和**工作流状态**（我现在在做什么，Plan-analyze / Plan-execute / Normal）。这种合并是 plan mode 落地后所有补丁的复杂度根源。R1 把它拆成两个独立的字段：`Preference` + `Workflow`，让后续的工作流扩展（plan / review / future）不再挤压 mode enum。

**状态**：📐 设计稿，未实施。重构 PRD，不引入用户可见的新能力 —— 但解锁后续 R2（capability declarations）/ R3（mode-aware registry）的清晰基底。

**触发起因**：2026-05 一次设计 review。在 v2 plan-mode + 6 个 v2.x 扩展（含 `preApproved` flag、`Action.ReadOnly` flag、bash 只读白名单等）落地后，盘账发现：
- `permission.Mode` 4 个值中，`ModePlan` 是**工作流状态**，其它 3 个是**用户偏好**
- `preApproved` flag 本质上是 plan-execute substate 专属字段，但因为没地方放它，浮在 `Policy` 顶层、要靠 `SetMode` 的 side effect 来同步清理
- `ModePlan ↔ ModeAsk` 切换被同时用来表达"plan-analyze ↔ plan-execute"，让 plan 子态机器隐式
- 每加一种工作流（未来可能的 review / dry-run / replay-debug）都要在 Mode enum 加一员，并污染所有 `switch mode { case ... }`

详细分析见 [`feature-plan-mode.md`](feature-plan-mode.md) §八 上下文 + 本 session 设计 review 讨论摘要。

---

## 一、目标与范围

### 1.1 这次改什么

把 `permission.Mode`（4 值）拆成两个正交的 enum：

```go
// Preference: 用户对 agent 行为的整体偏好（命令行 flag / TUI 切换驱动）
type Preference int
const (
    PrefDeny Preference = iota  // 拒绝一切危险动作（print mode 默认）
    PrefAsk                     // askFn 弹 y/N（TUI 默认）
    PrefYolo                    // 全放行（--yolo / 会话内升级）
)

// Workflow: 当前进入的工作流（命令 / 工具事件驱动）
type Workflow int
const (
    WorkflowNone Workflow = iota // 普通使用
    WorkflowPlanAnalyze          // /plan 后默认子态，read-only 调研
    WorkflowPlanExecute          // propose 批准后，可写但仍受 pref 约束
)
```

`Policy.Check(Action)` 改成**两阶段 gate**：

1. **Workflow gate**（外层）—— 工作流的硬约束。e.g. PlanAnalyze 拒一切写 / 拒非 ReadOnly bash，不管 Preference 是什么
2. **Preference gate**（内层）—— 用户偏好的常规处理（Yolo / Ask + askFn / Deny）

`preApproved` 这个 flag 显式属于 Workflow（仅在 PlanExecute 下有意义），不再是浮在 Policy 顶层的孤儿字段。

### 1.2 这次**不**改什么

- ❌ **不改 `Action` 字段**（Kind / Path / ReadOnly / Diff / Memory / Skill 等），那是 R2 的范围
- ❌ **不引入"capability 声明"**，不动 KindGit 特判，不统一 read-only 表达，那是 R2
- ❌ **不引入 mode-aware tool registry**（工具按 workflow 决定是否注册），那是 R3
- ❌ **不动 askFn / TUI 审批 UI / 用户可见行为**（理想情况下用户感知不到这次重构）
- ❌ **不引入新 Workflow 值**（PlanReview / DryRun 等留给后续），本 PRD 只覆盖已有的 plan-analyze / plan-execute
- ❌ **不改 session JSONL schema_version**（v2 保持，新增字段以 additive 兼容；详见 §3.4）

### 1.3 用户可见行为变更（应该是零）

| 路径 | 重构前 | 重构后 |
|------|--------|--------|
| `seek --plan` 启动 | `Mode = ModePlan` | `Pref = PrefAsk, Workflow = WorkflowPlanAnalyze` |
| `seek --yolo` 启动 | `Mode = ModeYolo` | `Pref = PrefYolo, Workflow = WorkflowNone` |
| TUI 输入 `/plan` | `cmdPlan` toggle ModePlan ↔ ModeAsk | `cmdPlan` toggle Workflow PlanAnalyze ↔ None |
| TUI 输入 `/yolo` | `cmdYolo` toggle ModeYolo ↔ ModeAsk | `cmdYolo` toggle Pref Yolo ↔ Ask |
| propose 批准 | TUI 收 `PlanProposalApproved` → 切 `ModeAsk` + label "plan-execute" | TUI 收同样事件 → 切 `Workflow = WorkflowPlanExecute`（pref 不动） |
| status bar `PLAN:ANALYZE` 等标签 | 读 `Plan + PlanSubstate` 字符串 | 读 `Workflow` enum（label 映射函数管渲染） |

外部表现 100% 一致。重构纯内部。

---

## 二、设计

### 2.1 新 Policy 结构

```go
type Policy struct {
    mu          sync.RWMutex
    pref        Preference
    workflow    Workflow
    cwd         string         // 同前
    askFn       func(Action) bool

    // preApproved 是 plan-execute workflow 的子状态。其它 workflow 下
    // 永远为 false。SetWorkflow 任何切换都会重置它，杜绝"workflow 变了
    // 但 flag 没清"的 bug。
    preApproved bool
}
```

**互斥关系由 SetWorkflow / SetPref 自己维护**：

- `SetPref(PrefYolo)` 不影响 workflow（理论上 yolo + plan-execute 是可表达的；但 TUI 的 cmdYolo 默认仍 enforce 互斥：把 workflow 设回 None。底层放开，UI 收口）
- `SetWorkflow(Workflow)` 总是 reset `preApproved`（任何工作流切换都使预批准失效，跟当前 SetMode 一致的安全姿态）

### 2.2 Check 重写

```go
func (p *Policy) Check(a Action) error {
    p.mu.RLock()
    pref := p.pref
    workflow := p.workflow
    cwd := p.cwd
    askFn := p.askFn
    preApproved := p.preApproved
    p.mu.RUnlock()

    // 1. Workflow gate (硬约束，先于 pref)
    if err := workflowGate(workflow, a, cwd); err != nil {
        return err
    }
    if workflow == WorkflowPlanExecute && preApproved {
        switch a.Kind {
        case KindBash, KindWrite, KindEdit:
            return nil  // step 内 batch fast-path
        }
    }

    // 2. Preference gate (常规)
    return prefGate(pref, a, cwd, askFn)
}

// workflowGate 返回 nil 表示工作流不阻止 (落到 pref gate)；
// 返回非 nil 表示工作流硬拒。
func workflowGate(w Workflow, a Action, cwd string) error {
    switch w {
    case WorkflowNone, WorkflowPlanExecute:
        return nil // 工作流不施加额外约束
    case WorkflowPlanAnalyze:
        return checkPlanAnalyze(a, cwd)
    }
    return nil
}

// prefGate 是原 Check 函数去掉 ModePlan 分支后的简化版。
func prefGate(pref Preference, a Action, cwd string, askFn func(Action) bool) error {
    if pref == PrefYolo {
        return nil
    }
    // ... dangerous-check loop（read inside cwd allow / write outside cwd dangerous / 等等）
    // pref == PrefAsk → askFn
    // pref == PrefDeny → 直接拒
}
```

这个拆分的关键收益：

- **`workflowGate` 是声明式的 read-only / read-write 规则集**，跟 Kind 矩阵脱钩
- **`prefGate` 是原 Check 的"非 plan"部分**，去掉了 ModePlan 整个分支后逻辑短一半
- `preApproved` 的检查点显式在 `WorkflowPlanExecute` 下面，不再是 Yolo 早返回之后浮着的一段

### 2.3 API 迁移（破坏性，一次性）

**删除的符号**（从 `permission` 包导出面消失）：

```go
type Mode int                    // 整个 enum 删
const ModeDeny / ModeAsk / ModeYolo / ModePlan  // 4 个常量删
func (p *Policy) SetMode(Mode)   // 删
func (p *Policy) Mode() Mode     // 删
```

**新增的符号**：

```go
type Preference int
const PrefDeny / PrefAsk / PrefYolo
type Workflow int
const WorkflowNone / WorkflowPlanAnalyze / WorkflowPlanExecute

func New(cwd string, pref Preference) (*Policy, error)  // 签名变了：原 Mode 参数 → Preference
func (p *Policy) Pref() Preference
func (p *Policy) Workflow() Workflow
func (p *Policy) SetPref(Preference)
func (p *Policy) SetWorkflow(Workflow)
```

**保留的兼容辅助**（为减小调用点 diff）：

```go
// 仍存在的 helper，语义不变：
func (p *Policy) Yolo() bool   // 现在返回 pref == PrefYolo
func (p *Policy) Plan() bool   // 现在返回 workflow != WorkflowNone
func (p *Policy) SetPreApproved(bool)  // 不变
func (p *Policy) PreApproved() bool    // 不变
```

**不保留 `Mode()` 兼容 alias**：故意。让编译错误把所有调用点逼出来一次性改干净，避免半新半旧拖很久。

### 2.4 调用点迁移盘点

`grep` 当前调用面：~60 个非测试引用，集中在 2 个文件：

#### `cmd/seek/main.go`（约 12 处 SetMode + 1 处 New）

```go
// 改前
policy, _ := permission.New(cwd, permission.ModeAsk)
policy.SetMode(permission.ModeYolo)
policy.SetMode(permission.ModePlan)

// 改后
policy, _ := permission.New(cwd, permission.PrefAsk)
policy.SetPref(permission.PrefYolo)
policy.SetWorkflow(permission.WorkflowPlanAnalyze)
```

`initialMode` 推断逻辑（main.go 头部）改为分别推 `initialPref` + `initialWorkflow`：

```go
initialPref := permission.PrefAsk
if *yolo { initialPref = permission.PrefYolo }
if printMode { initialPref = permission.PrefDeny }

initialWorkflow := permission.WorkflowNone
if *plan { initialWorkflow = permission.WorkflowPlanAnalyze }
```

`modeLabel(mode)` 函数（用于 system prompt）变成 `workflowLabel(pref, workflow)`，单点改造。

#### `internal/tui/commands.go`（cmdPlan / cmdYolo / cycleMode）

`cmdPlan`：toggle Workflow PlanAnalyze ↔ None，并通过 `SetPlan` callback 通知 host。退出时通过 `RevokePlanPreApproval` 清 preApproved（既存逻辑保留）。

`cmdYolo`：toggle Pref Yolo ↔ Ask。互斥（仍清 Plan workflow）。

`cycleMode`（Shift+Tab）：循环 pref（Ask → Yolo → Plan 现在变成 Ask pref → Yolo pref → 进入 PlanAnalyze workflow），需要小重写 — pref 和 workflow 两个轴循环 vs 单轴循环。可考虑改成只循环 pref，plan workflow 走 `/plan` 显式入口。

#### 三个 host callback（`SetYolo` / `SetPlan` / `SetPlanSubstate`）

签名保持不变，cmd/seek 端实现内部从"调 SetMode"改成"调 SetPref / SetWorkflow"。

#### `update_agent.go` 收 `PlanProposalApproved` 事件

```go
// 改前
m.opts.SetPlanSubstate("execute")  // → 上层 main.go: policy.SetMode(ModeAsk)

// 改后
m.opts.SetPlanSubstate("execute")  // → 上层 main.go: policy.SetWorkflow(WorkflowPlanExecute)
```

回调名 `SetPlanSubstate` 暂不改名（语义还合），但内部映射变了。

#### `mode_reminder`（pkg/agent/agent.go）

参数从 `ModeLabel string` 变成两个：`pref string, workflow string`。可以保留 `SetModeLabel(label string)` API 兼容（label 取最具体的那一项），或者推到下一次再清理。**v1 保持 SetModeLabel 单字段，cmd/seek 计算 label 时同时考虑 pref + workflow**。

#### Tests

`internal/permission/*_test.go`：~12 个测试用 `ModeXxx` 直接构造 Policy，机械替换为 `PrefXxx` + 必要的 `SetWorkflow` 调用。

`internal/tui/mode_test.go`：测 cmdPlan/cmdYolo 的，更新断言对象（从读 Mode 变成读 Workflow + Pref）。

### 2.5 mode_label / status bar 适配

`modeLabel(initialMode)` (cmd/seek/main.go:153) 改成：

```go
func modeLabel(pref permission.Preference, workflow permission.Workflow) string {
    // workflow 优先（更具体）
    switch workflow {
    case permission.WorkflowPlanAnalyze:
        return "plan-analyze"
    case permission.WorkflowPlanExecute:
        return "plan-execute"
    }
    if pref == permission.PrefYolo {
        return "yolo"
    }
    return "ask"
}
```

Status bar（`statusbar.go`）：当前 `StatusSnapshot` 同时有 `Yolo bool` + `Plan bool` + `PlanSubstate string`，已经是事实上的多轴表达。**不改 StatusSnapshot 的字段**（它是渲染契约，多组件读它）；只改 cmd/seek 端填充 snapshot 的方式（从 mode 推导改成从 pref + workflow 推导）。

---

## 三、关键决策

### 3.1 为什么不保留 `Mode` 兼容 alias

考虑过：

```go
// 旧 alias 保留半年
const ModeAsk = PrefAsk  // 但 PrefAsk 是 Preference 不是 Mode，类型不兼容
```

类型系统不会让我们这么搞（Mode 和 Preference 是不同类型）。除非：

```go
type Mode = Preference  // type alias
const ModeAsk = PrefAsk
const ModePlan = ???    // 没法 alias，Mode 是单值，Plan 现在是 Workflow
```

ModePlan 没有等价的 Preference 值（plan 是 workflow），强行 alias 会让"老 API 中 ModePlan 怎么映射"成为一个谜。**老 API 不可能与新 API 完全等价**，所以装作能 alias 反而误导。一刀切干净。

工作量上：60 个 grep 命中 → 全包内一次性 search-replace，配合编译错误清单走完，预计半天。比维护半年的双 API 划算。

### 3.2 为什么 workflow 作硬约束（先于 pref）

考虑过反过来：pref 优先，workflow 在 pref 允许后做额外检查。

被否决，理由：`PrefYolo + WorkflowPlanAnalyze` 应该**仍然只读**。这是"plan 模式的整个 raison d'être 是用户主动选择安全边界"。如果 pref 优先，Yolo + Plan 会变成 Yolo 直接放行 —— 破坏 plan 的安全契约。

更普遍的原则：**workflow 是用户*已经进入*的安全 ceremony；pref 是用户*愿意*的安全水位**。安全 ceremony 不该被偏好绕过。

### 3.3 为什么 SetWorkflow 重置 preApproved

跟现在 `SetMode` 重置 `preApproved` 是同款逻辑（CLAUDE.md "Mode + flag transitions default safe"）。任何 workflow 转换 = 该清场。`preApproved` 是 plan-execute 私有 flag，离开 plan-execute 后留着它毫无意义且危险。

### 3.4 为什么 session JSONL 不 bump schema_version

`session.Session` 现在有：

```go
Yolo bool `json:"yolo"`
Plan bool `json:"plan,omitempty"`
```

保持现状（不删、不改 JSON tag）。Save / Load 路径：

- **Save**：保留 `Yolo` / `Plan` 两个 bool 写出（计算自 pref + workflow）。Pref==Yolo → Yolo=true；Workflow!=None → Plan=true
- **Load**：读 `Yolo` / `Plan` 两个 bool，推导 pref + workflow。`Plan=true` 默认推 `WorkflowPlanAnalyze`（plan-execute 状态由 transcript 重建恢复，跟现状一致）

这样：

- ✅ 老 session 在新 binary 上加载完美
- ✅ 新 session 在老 binary 上加载完美（老 binary 看到熟悉的两个 bool，不知道有 workflow 概念也无所谓）
- ✅ 不引入 schema_version=3 的迁移压力

代价：Yolo + Plan 同时为 true 在新 binary 是可表达的（pref=Yolo, workflow=PlanAnalyze），在 save 后变成 `{Yolo: true, Plan: true}`，老 binary 看到会按"先 plan 后 yolo"的当前行为（plan 优先）。**这是真实差异**，但它发生在"用户主动构造的边角态"上，且当前 TUI 互斥逻辑会阻止用户达到这种状态。可接受。

### 3.5 为什么不顺便做 R2（capability 声明）

R2 影响 `Action` 结构，要改每个工具的 Check 调用站点（额外 ~20 个地方）。一起做的 PR 太大。R1 单独 ship 让代码 stable 一阵子，再开 R2。**小步快走优于一把梭**。

### 3.6 为什么 cycleMode（Shift+Tab）的循环顺序要重想

当前：Ask → Yolo → Plan → Ask。所有人都在一个 enum 里转，Shift+Tab 行为一致。

拆开后，Plan 不再是同轴。两个选项：

**(a)** 保持 3-步循环：Pref Ask → Pref Yolo → Workflow PlanAnalyze (pref 不动) → 回 Pref Ask + Workflow None。Shift+Tab 仍是单按键 step through，逻辑上是"用户安全水位"演进。

**(b)** Shift+Tab 只 cycle pref（2 步：Ask ↔ Yolo），plan 必须显式 `/plan`。更"语义干净"但破坏现有 muscle memory。

**v1 选 (a)**：保留肌肉记忆。`cycleMode` 函数内部做 pref+workflow 协同变更。代码复杂度略增，UX 不变。

---

## 四、非目标

- ❌ **新增 Workflow 值**（PlanReview / DryRun / Debug / …）—— 这些是 future PRD
- ❌ **改 askFn 签名 / 改 ApprovalRequest 形态**
- ❌ **改 TUI 审批 prompt 渲染**
- ❌ **把 Yolo + PlanExecute 暴露为一种"信任批准的 plan"产品形态**（底层支持，UI 不解锁；想要的话另起 PRD 讨论）
- ❌ **移除 Yolo() / Plan() 兼容方法**（外部调用方还在用，保留）
- ❌ **拆 Action.ReadOnly 到 capability**（R2 范围）
- ❌ **变 session 持久化为多字段**（保持两 bool 兼容）

---

## 五、风险与回滚

| 风险 | 概率 | 影响 | mitigation |
|------|------|------|--------|
| 调用点漏改 → 编译错误 | 高 | 拦在编译期，CI 兜底 | 故意不留 Mode alias，靠编译器找出 100% 调用站点 |
| 调用点改对了但语义错 → 行为漂移 | 中 | 用户看到 plan 模式行为差异 | 现有 `mode_test.go` / `permission_test.go` 全跑 -race；新增 4 个 cross-axis 测试（pref × workflow 笛卡尔积关键格） |
| Session 持久化的 Yolo+Plan 兼容性 | 低 | 老 session 在新 binary 加载行为变化 | §3.4 的双向兼容矩阵；保留两 bool 字段不动 |
| 老 binary 看到 plan + yolo 同时 true | 低 | 行为按"plan 优先"，但本就是 underspecified 状态 | 文档化为已知行为；TUI 互斥逻辑实际不会产生这种 session |
| cycleMode 顺序逻辑变复杂 | 中 | bug 滋生地 | 单独测 `TestCycleMode_*` 覆盖每个 transition |
| Shift+Tab 用户肌肉记忆 break | 低 | 用户抱怨 | §3.6 选 (a) 保留 3-步循环 |
| TUI Options 字段（Plan/PlanSubstate）需要联动改 | 中 | Options 是契约 | 不改字段名，只改填充逻辑（cmd/seek 端） |
| `agent.SetModeLabel` 单参数 vs 新 2 参数 | 低 | label 显示不完整 | v1 用单 label string，由 cmd/seek 计算最具体那个 |

### 5.1 回滚策略

R1 是单 PR 重构。失败回滚 = revert PR。session 持久化没动 schema，老 / 新 binary 互通，回滚 0 数据风险。

---

## 六、阶段交付

| 阶段 | 内容 | 工作量 |
|------|------|-------|
| P1 | `internal/permission/` 新类型 + Check 重写 + 互斥测试（pref × workflow 笛卡尔积） | ~150 行 + 测 ~200 行 |
| P2 | `cmd/seek/main.go` 调用站点改造（initialPref / initialWorkflow 推导、SetPref / SetWorkflow 调用、modeLabel 重写） | ~80 行 |
| P3 | `internal/tui/commands.go` cmdPlan / cmdYolo / cycleMode 重写 + 现有 mode_test.go 改对应断言 | ~60 行 + 测 ~50 行 |
| P4 | session save / load 兼容（Save 仍写 Yolo+Plan 两 bool；Load 推导 pref+workflow） | ~30 行 + 双向兼容测 |
| P5 | 端到端 smoke：plan-analyze → propose → plan-execute → adjust → cancel；--plan / --yolo CLI；status bar 标签三态；session resume 跨 binary | 半天 |
| P6 | CLAUDE.md / AGENTS.md "Permission model" 节更新（mode → preference + workflow） | ~10 行 |

**总计：~1 天**。建议一个大 PR 一次性 ship（兼容 alias 越界会拖很久；一把改干净最痛快）。

### 6.1 测试矩阵（pref × workflow 笛卡尔积关键格）

不需要全 3×3=9 格都测，关键的：

| pref | workflow | action | 期望 |
|------|----------|--------|------|
| Ask | None | bash | askFn 询问 |
| Ask | PlanAnalyze | bash w/o ReadOnly | 拒（工作流硬拒） |
| Ask | PlanAnalyze | bash w/ ReadOnly | 允许（go vet 等） |
| Ask | PlanAnalyze | write | 拒 |
| Ask | PlanExecute, !preApproved | write | askFn 询问 |
| Ask | PlanExecute, preApproved | write | 允许（fast-path） |
| Yolo | None | anything | 允许 |
| Yolo | PlanAnalyze | write | **拒**（workflow 先于 pref） |
| Yolo | PlanExecute | write | 允许（pref 接管） |
| Deny | * | dangerous | 拒 |

10 条用例足够覆盖语义。

---

## 七、v1 锁定的接口

后续 R2 / R3 不该破坏：

1. **`Preference` enum 值**（`PrefDeny / PrefAsk / PrefYolo`）—— 锁定
2. **`Workflow` enum 值**（`WorkflowNone / WorkflowPlanAnalyze / WorkflowPlanExecute`）—— 锁定；新 workflow 值可加
3. **`Check(Action) error` 签名** —— 锁定
4. **session JSONL 的 `Yolo` / `Plan` 两 bool 字段** —— 锁定（兼容旧）

未锁定（R2 / R3 可重做）：`Action` 的 ReadOnly / Kind 等字段、`workflowGate` / `prefGate` 内部实现、`cycleMode` 循环顺序。

---

## 八、与后续 R 系列的关系

| 重构 | 范围 | 依赖 |
|------|------|------|
| **R1（本 PRD）** | Mode → Pref + Workflow | 独立可 ship |
| R2 | Action.ReadOnly / KindGit / 各种 read-only 表达统一成 Capability 声明 | 依赖 R1（清晰的 workflow gate 后 capability 才好对接） |
| R3 | Tool registry 按 workflow 过滤可见工具 | 依赖 R1（registry 要看 workflow） |

R2 和 R3 是独立 PRD，待 R1 落地、代码 stable 后单独写。

---

## 九、相关文件

### 主要改动

- `internal/permission/permission.go` — 新类型 + Check 重写
- `internal/permission/permission_test.go` — 老 Mode 测试改名 + 新增笛卡尔积测试
- `internal/permission/preapproved_test.go` — `SetMode` 测试改为 `SetPref/SetWorkflow`
- `cmd/seek/main.go` — initialPref / initialWorkflow 推导，~12 处 SetMode 调用改 SetPref/SetWorkflow，modeLabel 函数重写
- `internal/tui/commands.go` — cmdPlan / cmdYolo / cycleMode 重写
- `internal/tui/mode_test.go` / `commands_test.go` — 断言对象改造
- `internal/session/session.go` — Save / Load 兼容逻辑（两 bool 字段保留）

### 文档更新

- `CLAUDE.md` / `AGENTS.md` — "Permission model" 节描述改成 "Preference + Workflow"
- `docs/prd/feature-plan-mode.md` — §2.5 permission 联动段落改写为新 API；§实施状态速览补 R1 条
- `docs/pitfalls.md` — 如果迁移过程踩到非显然坑，登记
