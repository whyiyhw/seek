# Feature: TUI 工效（Tab 补全 + 自定义 Keybindings）

**所属版本**：v3（柱 C · 可定制性）
**前置阅读**：[PRD v3 umbrella](v3.md) §3（跨柱约束）、[`internal/tui/commands.go`](../../internal/tui/commands.go)（cmdMeta 注册表）、[`internal/tui/update_key.go`](../../internal/tui/update_key.go)（按键分发）
**状态**：🚀 已交付（M9.4 + M9.5 / v0.4.2+）。`internal/keymap/`（自定义键位 + `~/.seek/keybindings.toml`）+ `internal/keyscli/`（`seek keys list/check/actions` CLI）+ Tab 补全（commands.go + 最近三次 polish fix `88e058b` `c26cc16` `3d0f254`）全部 ship。
**预估工作量**：~3 天（M9.4 + M9.5）

---

## 1. 动机

TUI 操作面板对所有人一样。重度用户（一天 3 小时以上 inline 交互）抱怨两件具体事：

- **斜杠命令没 Tab 补全**——要全名手敲 `/checkpoints`、`/skill`、`/distill`。当前已有 fuzzy 命令解析（命令未补全也能 dispatch），但**显示候选**仍要手敲完整命令；新用户记不住命令名时只能 `/help` 查表。
- **常用快捷键记不住又无法重绑**——`/steer` 中断、`/yolo` 切换这些组合键散在多个键位上，肌肉记忆不一致。Claude Code 有 `~/.claude/keybindings.json` 可自定义；seek 全是硬编码。

两件事在「能不能干活」上是零分，在「干活舒不舒服」上是满分。本 PRD **不重构 TUI 架构**——只在已有的 `cmdMeta` 注册表和 `update_key.go` 分发逻辑上加一层"补全 / 重映射"。

## 2. 设计目标与不做什么

### 目标

1. **复用现成 UI**——Tab 补全直接复用 `/skill use ...` 已经在用的 picker UI；keybindings 渲染复用 `/help` 浮层。
2. **封闭 action 集合**——keybindings 只允许重绑已有动作，不允许"key → 任意 shell 命令"，避免把 TUI 退化成 emacs 配置文件。
3. **启动时硬校验**——keybindings.toml 语法错 / action 错 / key 错 → 启动报错（stderr 打印 + fallback 默认）；不静默忽略。
4. **`/help` 显示当前生效的 keymap**——用户改过的绑定应该立刻可见，不依赖记忆。
5. **零打扰**——补全候选用 picker（用户主动 Tab 才弹），不在输入框边上做 ghost text 类干扰式 UI。

### 不做什么（v3 明确延后）

- ~~❌ **子命令补全**——`/skill <verb>` 的 verb 候选（install/list/...）不在 v3 范围内~~ **（已交付）**：`/skill` 走 bespoke verb→name 二级 picker；`/memory`、`/hooks` 等 CLI 镜像命令走 `command.subcommands` 数据驱动的通用 verb picker（`updateCommandMenu` 的 generic 分支，purpose 前缀 `subcmd:`）。新增一个有 verb 的命令只需在 `allCommands()` 填 `subcommands`，无需新分支。
- ❌ **参数补全**——只补到斜杠命令名 + 自动加空格；后面参数靠 v0 已有的路径补全或手敲。
- ❌ **Chord（多键序列）keybindings**——bubbletea 不原生支持 stateful chord handler；用户调研也只是"想换两个组合键"而不是"想要 vim 模式"。v3 不投入。
- ❌ **"key → 任意 shell 命令"**——keybindings 只能重绑封闭 action 集合，避免变成可执行配置（安全 + 学习成本 + 跨平台差异）。
- ❌ **GUI 配置工具**——keybindings.toml 手编辑；`/help` 浮层只读显示，不内置编辑器。
- ❌ **重绑保留 key**——`enter`（换行）、`tab`（补全）不允许重绑到其他 action，避免输入框无法工作。

## 3. Tab 补全

### 3.1 触发条件

TUI 输入框处于第一行、buffer 以 `/` 开头时，按 Tab 触发补全。其他情况（buffer 不以 `/` 开头 / 不在第一行 / 多行模式）Tab 维持原语义（插入 tab 字符或跳焦点）。

### 3.2 候选源

现有 `cmdMeta` 注册表（`internal/tui/commands.go` 第 51 行起的 `[]cmdSpec`）——补全候选已经是 source of truth，零额外维护。新增 slash 命令时自动出现在补全列表。

### 3.3 行为

| 输入 | 按 Tab 后的行为 |
|---|---|
| `/u` （唯一前缀匹配 `/undo`） | 直接补全为 `/undo `（含尾空格） |
| `/sk`（多候选：`/skill` / `/skills`） | 弹 picker，候选按 `description` 排序 |
| `/zzz`（无匹配） | 终端 bell + 不变 |
| `/skill ` 后（已是完整命令） | bell + 不变（子命令补全留给 v4） |

### 3.4 Picker 集成

picker 弹出后：

- ↑/↓ 选择
- Enter 确认 → 输入框替换为该命令 + 尾空格
- Esc 关闭 → 输入框不变
- 持续输入 → picker 实时过滤（已有能力）

## 4. 自定义 Keybindings

### 4.1 配置文件

```
~/.seek/keybindings.toml        # 仅用户级——keybinding 是个人偏好，不进 git
```

**为什么没有项目级**：keybindings 是个人 muscle memory，不该跟项目绑定。强制项目级会让团队成员被迫接受不熟悉的键位。

### 4.2 Schema

```toml
# ~/.seek/keybindings.toml

[bindings]
submit          = "ctrl+enter"
interrupt       = "alt+enter"
steer           = "alt+s"
clear-input     = "ctrl+l"
toggle-help     = "f1"
toggle-plan     = "f2"
toggle-yolo     = "f3"
expand-input    = "ctrl+up"
history-prev    = "ctrl+p"
history-next    = "ctrl+n"
quit            = "ctrl+q"
```

未列出的 action 自动 fallback 到默认 binding。

### 4.3 封闭 action 集合

v3 暴露的 actions 等于「当前 TUI 已经响应的全部硬编码按键」——把 `internal/tui/update_key.go` 里所有 `tea.KeyMsg` 分支抽出常量名，作为可重绑 action：

| Action 名 | 默认 key | 说明 |
|---|---|---|
| `submit` | `enter` 单行 / `ctrl+d` 多行 | 提交输入 |
| `interrupt` | `ctrl+c` | 中断当前 agent 回合 |
| `steer` | `alt+enter` | 提升排队 message 为中断 |
| `clear-input` | `ctrl+u` | 清空输入框 |
| `toggle-help` | `?` | 打开/关闭 help 浮层 |
| `toggle-plan` | `shift+tab` | `/plan` 切换 |
| `toggle-yolo` | `(无默认)` | `/yolo` 切换 |
| `expand-input` | `alt+e` | 展开输入框为多行 |
| `history-prev` | `↑` | 历史 prompt 上一条 |
| `history-next` | `↓` | 历史 prompt 下一条 |
| `quit` | `ctrl+c ctrl+c` | 退出 seek |
| `scroll-up` | `pgup` | scrollback 上翻 |
| `scroll-down` | `pgdn` | scrollback 下翻 |
| `cancel-picker` | `esc` | 关闭当前 picker |

**完整清单**在 `internal/keymap/actions.go` 维护为单一常量数组——加新 action 时同时更新数组 + `update_key.go` 分发逻辑。

### 4.4 解析与冲突检测

启动时解析 `keybindings.toml`：

1. **toml 语法错** → stderr 打印错误位置 + 全 fallback 到默认；TUI 启动继续
2. **action 名拼错**（如 `submmit`）→ stderr 打印 "unknown action: submmit; available: submit, interrupt, ..."；该条忽略，其他条目继续生效
3. **key 表达 bubbletea 不识别**（如 `cmd+enter`，macOS-only）→ stderr 打印 "unrecognized key: cmd+enter; see `seek keys list` for available keys"；该条忽略
4. **同一 key 被多个 action 占用** → stderr 打印冲突详情；**整个文件作废**（fallback 全默认），避免行为不可预测
5. **保留 key 被重绑**（`enter` 单行 / `tab` 任意场景）→ stderr 打印 "reserved key: enter cannot be rebound"；该条忽略

**`seek keys check`**：干跑校验整个文件，不启动 TUI，给 CI / 配置同步用。

### 4.5 `/help` 显示当前生效 keymap

`/help` 浮层渲染时调用 `keymap.Snapshot()` 拉当前生效的 (action → key) 映射；用户改过的 binding 立刻可见，不依赖记忆。

## 5. CLI 与 TUI 命令

### 5.1 CLI

```
seek keys list [--json]
    打印当前生效的 keymap（合并用户配置 + 默认）；标注每条是 "user" 还是 "default"。

seek keys check [<path>]
    校验 keybindings.toml 语法 + action 名 + key 名。
    无参数：检查 ~/.seek/keybindings.toml
    带 path：检查指定文件（用于 CI / 配置同步）
    exit code: 0 通过 / 2 有错（错误细节走 stderr）

seek keys actions [--json]
    打印封闭 action 集合 + 默认 key + 描述（给用户起手时参考）。
```

### 5.2 TUI slash 命令

```
/keys                               等价于 seek keys list（浮层显示）
```

Tab 补全没有 slash 命令——它是按键行为而非命令。

## 6. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/keymap`（新包） | keybindings.toml 解析、action 注册表、`Resolve(tea.KeyMsg) Action` | 小-中 |
| `internal/tui/update_key.go` | 把硬编码的 `tea.KeyEnter` / `tea.KeyCtrlC` 等替换为 `keymap.Resolve(msg)`；分支逻辑不变 | 中（机械替换） |
| `internal/tui/commands.go` | 注册 `/keys` slash 命令；在 `parseInput` 入口加 Tab 处理分支调 picker | 中 |
| `internal/tui/view.go`（help 浮层）| 把硬编码的 keymap 表格改为 `keymap.Snapshot()` 渲染 | 小 |
| `internal/paths` | 新增 `UserKeybindings()` | 极小 |
| `cmd/seek` | 新增 `keys` 子命令 | 小 |
| `pkg/agent` | **不动** | 0 |
| Session 文件格式 | **不变** | 0 |

### 6.1 Prefix cache 影响

| 机制 | 是否进 prompt 字节序列 | 是否影响 cache |
|---|---|---|
| Tab 补全 | 否（纯 UI 操作） | 否 |
| Keybindings | 否（按键路由变了，但用户输入文本不变） | 否 |

## 7. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | TUI 输入 `/u` + Tab 唯一补全到 `/undo `（含尾空格） | 集成测试（bubbletea testkit） |
| 2 | TUI 输入 `/sk` + Tab 弹 picker，候选含 `/skill` 和 `/skills` | 集成测试 |
| 3 | TUI 输入 `/zzz` + Tab 不变 + 终端 bell | 集成测试 |
| 4 | Buffer 不以 `/` 开头时 Tab 维持原语义 | 集成测试 |
| 5 | 新增 slash 命令到 `cmdMeta` 后自动出现在补全列表 | 单元测试 |
| 6 | 同一 key 被两个 action 占用时 keybindings.toml 启动报错且 fallback 到默认 | 单元测试 |
| 7 | action 名拼错时该条忽略 + 其他条目继续生效 | 单元测试 |
| 8 | 重绑 `submit = "ctrl+enter"` 后实际按 ctrl+enter 触发提交 | 集成测试 |
| 9 | `/help` 浮层显示用户改过的 binding（不是默认） | 集成测试 |
| 10 | `seek keys check` 对损坏的 toml 返回 exit 2 + stderr 错误 | 集成测试 |
| 11 | `enter` / `tab` 被显式重绑时启动报错（保留 key） | 单元测试 |
| 12 | 现有 v0/v1/v2 测试套件零回归（特别是 `update_key.go` 机械替换后） | 现有测试 |

## 8. 实现计划

| 子 ms | 内容 | 估时 |
|---|---|---|
| **M9.4** | `internal/keymap` 包 + 替换 `update_key.go` 硬编码 + `/help` 渲染 keymap + `seek keys list/check/actions` CLI | ~2 天 |
| **M9.5** | Tab 补全：parseInput 入口 + cmdMeta 候选 + 复用 picker UI | ~1 天 |

**发版策略**：

- M9.4 + M9.5 合并为 **v0.4.2**——工效改进打包；任意一个砍掉都不影响另一个上线
- 如果时间紧，**优先 ship M9.4（keybindings）** ——补全有现成 fuzzy 解析兜底，工效收益边际较小

**与其他 feature 的并行关系**：

- 与 [`feature-checkpoint.md`](feature-checkpoint.md) 仅 `cmdMeta` 注册条目层面有交集，无冲突
- 与 [`feature-shell-hooks.md`](feature-shell-hooks.md) 完全独立

## 9. 风险

| 风险 | 缓解 |
|---|---|
| 机械替换 `update_key.go` 引入回归（漏改某个分支或改错条件） | 替换前先把 `update_key.go` 的所有分支抽成表驱动测试（输入 KeyMsg → 期望 action），替换后跑相同测试集应全绿 |
| 用户重绑 `submit = "enter"` 而 `enter` 是换行 → 输入框无法工作 | 保留 key 列表（`enter` 作为换行，`tab` 作为补全）不允许绑定其他 action；硬冲突报错 |
| bubbletea 不识别某个 key 字符串（如 `cmd+enter` macOS-only） | keybindings.toml 解析时一次性 dump 可识别 key 集合到 stderr；用户照抄；`seek keys actions` 也列出 |
| Tab 补全跟用户 IME / 终端的 Tab 行为冲突 | 只在 buffer 以 `/` 开头时拦截 Tab；其他时候让 bubbletea 默认处理 |
| picker 弹出时用户输入快速键序列导致 UI 状态混乱 | 复用现有 picker 的状态机（已经在 `/skill use` 流程里验证过） |
| 多终端 (iTerm / Alacritty / VS Code terminal) 对组合键识别不一致 | 文档列出已知差异（如 `ctrl+enter` 在某些终端被吞）；建议用户 `seek keys check` 验证 |
| 删除 `keybindings.toml` 后用户期望立刻回默认，但还要重启 TUI | 文档说明配置只在启动时加载；v4 考虑 SIGHUP 重载（按 `r` reload）|

## 10. 后续版本

- **v4**：子命令补全（`/skill <verb>` / `/memory <verb>` 的候选数据驱动）
- **v4**：路径补全在斜杠命令参数位置（如 `/restore <文件路径>` 补全）
- **v4**：Chord keybinding（如果 v3 的"单键不够用"反馈强烈，给 `g g` 这类 vim 风格序列开口子）
- **v4**：keybindings.toml 热重载（SIGHUP / 按 `r`）
- **v5**：可视化键位编辑器（TUI 内的 record-and-bind 流程）
