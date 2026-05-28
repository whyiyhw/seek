# 功能对比：Seek vs. Claude Code

> **最后更新**：2026-05-27
> **数据来源**：[Claude Code 官方文档](https://code.claude.com/docs/en/overview)（sitemap: 130+ 页面，周 changelog 覆盖 2026 W13–W20）+ 在 Claude Code 内可直接观察到的工具/skill 清单；Seek 代码库主干。
> **核查范围**：Seek 一侧所有功能均回到代码核对；Claude Code 一侧仅保留有官方文档或直接观察证据的条目，存疑项已标注 "(待确认)"。

系统对比 **Seek**（开源、DeepSeek 优先、多 LLM 智能体）与 **Claude Code**（Anthropic 的自主编程智能体参考实现）。

## 图例

| 标记 | 含义 |
|---|---|
| ✅ **对等** | 功能存在，实现水平相当 |
| 🔶 **等效替代** | 不同实现方式，达成相同功能目标 |
| ❌ **缺失** | 没有直接等价物 |

---

## 1. 核心 Agent 循环与执行

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 基础工具循环（读/写/bash/edit 等） | ✅ | ✅ | **对等** | Seek：`read`、`write`、`edit`、`bash`、`grep`、`list_dir`、`git`、`think`、`fim_complete`，另有 `ask_user` 选择器 |
| FIM / 自动补全 | ✅ | ✅ | **对等** | Seek：通过 DeepSeek FIM 端点提供 `fim_complete` 工具（小范围编辑比 chat 便宜） |
| Think / 反思 | ✅ | ✅ | **对等** | Seek：`think` 工具调用 DeepSeek reasoner；内置 `dual-model` skill 实现 reasoner→执行→反思闭环 |
| LSP 工具（结构化符号/引用） | ✅ | ❌ **缺失** | — | Claude Code 暴露 `LSP` 工具直连语言服务器拿 hover / def / refs；Seek 所有"找符号"路径都走 grep+read |
| Notebook 编辑 | ✅ | ❌ **缺失** | — | Claude Code 有 `NotebookEdit` 原生编辑 Jupyter `.ipynb`；Seek 无 |
| 后台 bash + 流式监听 | ✅ | ❌ **缺失** | — | Claude Code Bash 支持 `run_in_background`，配 `Monitor` 流式跟 stdout；Seek bash 仅一次性 exec |
| Web 搜索 | ✅ | ❌ **缺失** | — | Claude Code `WebSearch`；Seek 只有 `webfetch`（HTTPS GET 单页） |
| Fast 模式 | ✅ | ❌ **缺失** | — | Claude Code 有 `/fast` 模式，对简单任务快速低消耗响应 |
| Headless / 非交互模式 | ✅ | 🔶 **等效替代** | Seek 有 JSON-RPC 2.0 service 模式（`--rpc`）用于 IDE 接入和 `-json` / `-p` 一次性输出，但不是通用 `--headless` 标志 |
| 会话持久化 | ✅ | ✅ | **对等** | JSONL 格式（`schema_version=2`），自动保存，`/branch` 分支，`/compact` 压缩，断点续传 |
| 上下文窗口管理 | ✅ | ✅ | **对等** | `/compact` 压缩，DeepSeek 前缀缓存（实测 95.7% 命中率） |
| 交互式 / 流式 | ✅ | ✅ | **对等** | inline TUI（bubbletea），流式输出，`ask_user` TUI 选择器 |
| 自动继续 / steer | ✅ | ✅ | **对等** | Seek：`/steer` 中途插入指令，`AutoContinue` 模式 |
| 多行输入 | ✅ | ✅ | **对等** | Textarea 输入，路径自动补全 |

**小结**：核心 chat/edit 循环对等。真正的工具集差距集中在四件套：**`LSP`**（结构化代码查询）、**`NotebookEdit`**（notebook 编辑）、**`Monitor` + 后台 Bash**（长任务跟踪）、**`WebSearch`**（不止 fetch 单页）。Fast 模式属于 UX 便利功能而非架构级特性。

---

## 2. 计划与权限系统

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| Plan / 批准工作流 | ✅ | ✅ | **对等** | Seek：`analyze → propose → execute → report`，通过 `propose` 和 `plan` 工具驱动 |
| 权限模式 | ✅ | ✅ | **对等** | Seek：`deny` / `ask` / `yolo` / `plan`（对应 Claude Code 的 Ask / Auto / Plan / Yolo） |
| 逐步自动批准 | ✅ | ✅ | **对等** | Seek：`plan(start)` 设置的 `preApproved` 标志 |
| 只读白名单 | ✅ | ✅ | **对等** | Seek：`Action.ReadOnly` 标记无副作用命令，plan 模式放行 |
| 续传恢复计划状态 | ✅ | ✅ | **对等** | Seek：通过 transcript 事件溯源（`plan/reconstruct.go`）恢复状态 |
| 撤销 / 回滚 | ✅ | ✅ | **对等** | Git 基础撤销；Plan 模式拒绝后回退 |

**小结**：计划/权限对等。Seek 的权限模型有公开的设计文档（四种模式 + `preApproved` / `Action.ReadOnly` 两个标志 + 线格式计划标记），实现细节相对透明；Claude Code 等价机制存在但实现细节未公开。

---

## 3. 上下文、记忆与指令

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 项目指令（`CLAUDE.md`） | ✅ | ✅ | **对等** | Seek 同时读取 `AGENTS.md` 和 `CLAUDE.md`（两者为独立文件，靠人工同步维护） |
| `.claude/` 目录 | ✅ | ✅ | **对等** | Seek 扫描 4+1 层：`.seek/` > `.claude/` > `~/.config/seek/` > `~/.claude/` > 内置 |
| 持久化记忆 | ✅ | 🔶 **等效替代** | 两侧都做了，但模型不同：Claude Code 用类型化条目（user/feedback/project/reference）+ `MEMORY.md` 索引；Seek 用三层 L/M/S（L=跨项目偏好，M=项目决策，S=session JSONL）|
| 跨会话蒸馏 / 提炼 | ✅ | ✅ | **对等** | Seek：`seek -dream`（跨项目用户偏好归纳）+ `/distill`（会话→项目决策提炼）|
| 记忆 GC / 自动遗忘 | 🔶 | ✅ | **Seek 优势** | Seek：decay-score GC 自动归档过时 M 条目；Claude Code 侧需要手动维护 |
| Prompt 缓存 | ✅ | ✅ | **对等** | DeepSeek 前缀缓存（实测 95.7%，见 README）；Claude Code 用 Anthropic prompt cache |

**小结**：记忆方向一致但模型不同。Seek 的 `-dream` 跨项目归纳和 decay-score 自动遗忘在公开实现上更前置；Claude Code 类型化条目（feedback / project / reference）则在结构化检索上更细。

---

## 4. 工具集成：MCP

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| MCP 客户端（stdio） | ✅ | ✅ | **对等** | JSON-RPC 2.0 over stdio，Bridge 模式；实现位于 `internal/mcpconfig/` + `internal/tools/mcptool/` |
| `.mcp.json` 配置 | ✅ | ✅ | **对等** | 与 Claude Code / Cursor 兼容的格式 |
| MCP 工具自动发现 | ✅ | ✅ | **对等** | 启动时加载；单 server 错误不阻塞其他 |
| 名称冲突解决 | ✅ | ✅ | **对等** | `<server>__<tool>` 前缀 |
| Managed MCP（中心化管理） | ✅ (待确认) | ❌ **缺失** | — | 企业级中心化管理的 MCP server；属企业平台功能，未在公开 CLI 中直接验证 |
| 从 TUI 重启 MCP server | ✅ | ❌ **缺失** | — | Claude Code 可在工具内重启 MCP server |

**小结**：MCP 协议层完全对等。差距在于企业管理面（Managed MCP）和便利运维（TUI 内重启），都非协议级缺失。

---

## 5. 工具集成：Skills

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| Anthropic Agent Skills 格式 | ✅ | ✅ | **对等** | `SKILL.md` + YAML frontmatter；单文件 `.md` 和目录包并存 |
| 从 Git URL 安装 | ✅ | ✅ | **对等** | `seek skill install https://github.com/...` |
| 从压缩包/本地路径安装 | ✅ | ✅ | **对等** | 完整 `skill_fetch` / `skill_commit` 工作流 |
| 4+1 层路径优先级 | ✅ | ✅ | **对等** | 项目 > 用户 > 内置，含 Claude Code 兼容路径 |
| Frontmatter 解析（标量/列表/块） | ✅ | ✅ | **对等** | Seek 的解析器用 ~170 行 Go 处理 4 种形状，零依赖 |
| Skill 生命周期（创建/统计/更新） | ✅ | ✅ | **对等** | `seek skill create`、`stats --top 5`、外部安装 |
| 调用统计 JSONL | ✅ | ✅ | **对等** | `internal/skillstats/` 追踪每次调用 |
| 用户 vs 项目作用域 | ✅ | ✅ | **对等** | `~/.seek/skills/` vs `<project>/.seek/skills/`，安装需审批 |

**小结**：Skills 完全对等。Seek 在 Skill 生命周期上投入了重点工程（M8 里程碑）。

---

## 6. 工具集成：Hooks 与插件

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 生命周期 hooks（会话开始/结束） | ✅ | ✅ | **对等** | Seek：`hooks.Registry` 含 `SessionStart`、`SessionEnd`、`PrePromptHook` |
| Pre/post 命令 hooks | ✅ | ✅ **v0.4.1** | **对等** | Seek：`.seek/hooks.toml` 可配置 `pre_tool` deny + 五个 observer |
| 插件系统 | ✅ | ❌ **缺失** | — | Claude Code 有插件市场和依赖管理 |
| 插件 hints / 依赖 | ✅ | ❌ **缺失** | — | 包管理器式插件元数据 |

**小结**：生命周期 hooks 和 pre/post 命令 hooks 均已对等。插件系统完全缺失。

---

## 7. 开发工作流

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| Git 读操作 | ✅ | ✅ | **对等** | Seek：`git` 工具（只读，plan 模式白名单） |
| Git commit / push | ✅ | ✅ | **对等** | Seek：通过 `bash`（需审批） |
| 自动 git checkpoint | ✅ | ✅ **v0.4.0** | **对等** | Seek：`internal/checkpoint` git ref 命名空间，每 turn 首次破坏操作前自动快照 |
| 文件 checkpointing（undo/redo） | ✅ | ✅ **v0.4.0** | **对等** | Seek：content-addressed CAS blob undo/redo，`/undo` `/redo` / `seek undo` |
| Code review（`/review`） | ✅ | ✅ | **对等** | Seek：`/review` 激活 plan + review prompt |
| Ultra review（云端多 agent 深度复审） | ✅ | ❌ **缺失** | — | Claude Code：`/code-review ultra`（旧名 `/ultrareview`），调用云端多 agent 协作复审 |
| GitHub PR 工作流 | ✅ | 🔶 **等效替代** | Claude Code 通过 `gh` CLI + Bash 创建/审查/合并 PR（无独立的 `/pr-create` 斜杠命令）；Seek 同样可通过 `gh` 经 `bash` 工具达成，但缺少为 PR 流程专门优化的 prompt/skill |
| Worktree 管理 | ✅ | ❌ **缺失** | — | Claude Code 提供 `EnterWorktree`/`ExitWorktree` 工具用于并行任务隔离 |
| Tab 补全（TUI 命令） | ✅ | ✅ **v0.4.x (M9.5)** | **对等** | Seek：斜杠命令 Tab 补全；唯一匹配直接 accept，多匹配填到最长公共前缀 |
| Shell 命令 hooks | ✅ | ✅ **v0.4.1** | **对等** | Seek：`.seek/hooks.toml` 可配置 `pre_tool` deny + 五个 observer |

**小结**：开发工作流主要差距收敛到 **worktrees** 与 **PR 流程优化 prompt**。自动 git checkpoint、文件级 undo/redo 已在 v0.4.0 补齐，斜杠命令 Tab 补全在 v0.4.x（M9.5）补齐。

---

## 8. UI/UX 与交互

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| Inline TUI 模式 | ✅ | ✅ | **对等** | 双方都用 tea.Println 写 scrollback + 实时渲染 |
| 状态栏 | ✅ | ✅ | **对等** | 模型、费用、缓存命中率、错峰倒计时 |
| `/` 斜杠命令 | ✅ | ✅ | **对等** | Seek 已覆盖 `/help` `/clear` `/model` `/effort` `/yolo` `/plan` `/review` `/branch` `/compact` `/distill` `/skill` `/skills` `/memory` `/steer` `/setup` `/upgrade` 等 16 个 |
| 用户提问选择器 | ✅ | 🔶 **等效替代** | Claude Code 用结构化 `AskUserQuestion`（一次 1–4 题、每题 2–4 选项、可 multiSelect、可带 preview 双栏渲染、自动 Other 自由输入）；Seek 用单选 `ask_user` picker，能力差一档 |
| `help` 浮层 | ✅ | ✅ | **对等** | 双方都有可关闭的浮层 |
| 快捷键绑定（可自定义） | ✅ | ✅ **v0.4.x (M9.4)** | **对等** | Seek：`~/.seek/keybindings.toml` + `internal/keymap` / `internal/keyscli`（`seek keys check` 可校验） |
| 输出样式 | ✅ | ❌ **缺失** | — | Claude Code 有多种输出样式预设 |
| 语音输入 | ✅ (待确认) | ❌ **缺失** | — | macOS 终端层面的语音输入是 OS 能力，是否在 Claude Code 内有专门集成存疑 |
| Computer use（GUI 自动化） | ✅ (API 层) | ❌ **缺失** | — | Anthropic Computer Use 是 API 能力，在 Claude Code CLI 内的暴露形式未直接验证 |
| Chrome 扩展 | ✅ | ❌ **缺失** | — | 浏览器集成，获取 Web 上下文 |
| IDE 集成（VS Code / JetBrains） | ✅ | 🔶 **等效替代** | Seek 通过 `--rpc` JSON-RPC 2.0 服务暴露给编辑器；不是完整的 IDE 扩展 |
| 桌面应用 | ✅ | ❌ **缺失** | — | Claude Code Desktop（Electron） |
| Web 界面 | ✅ | ❌ **缺失** | — | Claude Code on the Web |

**小结**：核心 TUI 对等，快捷键自定义已在 v0.4.x（M9.4）补齐；用户选择器仍是 Claude Code 表达力领先（结构化多题/多选项/preview）。语音、computer use、IDE 扩展、桌面/Web 等属于多端平台战略而非 Agent 核心。

---

## 9. LLM 与成本管理

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 多提供商支持 | ❌（仅 Anthropic） | ✅ | **Seek 优势** | DeepSeek、Anthropic、OpenAI、Gemini、OpenAI 兼容端点 |
| DeepSeek 专属能力 | ❌ | ✅ | **Seek 优势** | V4 推理模式、FIM 端点、错峰 5 折、前缀缓存追踪 |
| 费用追踪（每会话） | ✅ | ✅ | **对等** | Seek：累计 + 每轮费用，状态栏实时显示 |
| 缓存命中率显示 | ✅ | ✅ | **对等** | Seek：实时命中率 + 节省 token 数 |
| 错峰折扣倒计时 | ❌ | ✅ | **Seek 优势** | 状态栏显示 5 折时段（北京时间 00:30–08:30） |
| 定价对比 | ❌ | ✅ | **Seek 优势** | README 中的多提供商定价矩阵 |

**小结**：LLM/成本方面 Seek 完胜——提供商自由 + DeepSeek 专属优化。Claude Code 绑定 Anthropic。

---

## 10. 高级 Agent 功能（多 agent / 调度 / 远程）

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 子代理（`Agent` 工具，可指定 `subagent_type`） | ✅ | ❌ **缺失** | — | Claude Code 可 spawn 子 agent 并行/隔离执行任务，支持 `worktree` 隔离 |
| 子代理类型库（Explore / Plan / general-purpose 等） | ✅ | ❌ **缺失** | — | 内置专用子 agent 类型 |
| 定时任务 / Routines（`CronCreate` + `/schedule`）| ✅ | ❌ **缺失** | — | Claude Code 把 cron 调度的远程 agent 称为 "routines"；与下方"定时任务"是同一物，对外接口是 `/schedule` skill |
| 远程触发 / PushNotification | ✅ | ❌ **缺失** | — | `RemoteTrigger` / `PushNotification` 工具，连接远程开发环境与消息通道 |

**小结**：最大差距区，全是多 agent / 调度 / 远程方向。Seek 目前是单进程单 agent 设计，没有 spawn / 调度 / 远程触发机制。

---

## 11. 平台与部署

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 单二进制（零运行时依赖） | ❌（Node/npm） | ✅ | **Seek 优势** | ~5 MB Go 二进制，零依赖 |
| 跨平台（macOS/Linux/Windows） | ✅ | ✅ | **对等** | 三平台全部支持 |
| 自托管部署 | ✅ | ❌ **缺失** | — | Claude Code 可在自有基础设施上自托管 |
| Devcontainer 检测 | ✅ | ❌ **缺失** | — | 自动检测并在 dev container 中运行 |
| Docker 沙箱执行 | ✅ | ❌ **缺失** | — | 容器中隔离执行命令 |
| CI/CD 集成（GitHub Actions） | ✅ | ❌ **缺失** | — | 原生 CI pipeline 支持（Headless 模式见第 1 节，不重复列出） |

**小结**：平台层各有千秋。Seek 胜在简洁（单 Go 二进制）；缺在自托管、容器、CI/CD。

---

## 12. 管理与企业功能

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| SSO / 认证 | ✅ | ❌ **缺失** | — | 企业身份集成 |
| 用量分析 / 监控 | ✅ | ❌ **缺失** | — | 用量仪表盘、监控 |
| 服务端管理配置 | ✅ | ❌ **缺失** | — | 中心化策略管理 |
| 零数据保留选项 | ✅ | ❌ **缺失** | — | 合规功能 |
| 数据使用控制 | ✅ | ❌ **缺失** | — | 管理员数据治理 |

**小结**：企业功能 Seek 全部缺失。Seek 定位为单用户本地工具，企业不是目标。

---

## 13. 可扩展性与 SDK

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| TypeScript SDK | ✅ | ❌ **缺失** | — | 用于构建自定义工具/agent |
| Python SDK | ✅ | ❌ **缺失** | — | 用于构建自定义工具/agent |
| Agent View（监控界面） | ✅ | ❌ **缺失** | — | Agent 活动的 Web UI |
| 结构化输出 | ✅ | ✅ | **对等** | 工具 JSON 输出 |

**小结**：Seek 不是 SDK——它本身就是 agent，不是构建 agent 的框架。

---

## 差距汇总

### 按投入产出比的差距清单

优先级仅供路线图参考，不代表 Claude Code 用户使用频率（缺乏 Seek 侧的真实需求数据来佐证排序）。

| 优先级 | 领域 | 缺失功能 | 备注 |
|---|---|---|---|
| **🟢 已交付** | 工作流 | **自动 git checkpoint** | v0.4.0：每 turn 首次破坏前自动 git ref 快照 |
| **🟢 已交付** | 工作流 | **文件级 checkpoint（undo/redo）** | v0.4.0：content-addressed CAS undo/redo，`/undo` `/redo` |
| **🟢 已交付** | Hooks | **Pre/post 命令 hooks** | v0.4.1：`.seek/hooks.toml` 可配置，`pre_tool` deny + 五个 observer |
| **🟢 已交付** | UI | **Tab 补全（斜杠命令）** | v0.4.x（M9.5）：唯一匹配 accept、多匹配填最长公共前缀 |
| **🟢 已交付** | UI | **快捷键绑定** | v0.4.x（M9.4）：`~/.seek/keybindings.toml` + `keys check` 校验 |
| **🔴 P0** | Agent | **子代理 / `Agent` 工具** | 架构级缺口；Claude Code 支持 `subagent_type` + 子上下文隔离 + 可选 worktree。并行探索 / 长上下文保护 / 角色专精都依赖这一层 |
| **🔴 P0** | 调度 | **Routines / 远程触发（`CronCreate` / `ScheduleWakeup` / `RemoteTrigger` / `PushNotification`）** | 架构级缺口；与子代理共享调度面，做应一起设计 |
| **🔴 P0** | 工作流 | **Worktrees（`EnterWorktree`/`ExitWorktree`）** | 与子代理强耦合：子 agent 默认在隔离副本工作 |
| **🟡 P1** | UI | **`AskUserQuestion` 结构化选择器** | 多题/多选项/preview 双栏渲染，对"让模型自己拿决定"路径影响大 |
| **🟡 P1** | 工具 | **LSP 工具** | 结构化符号/引用查询，大型代码库 grep 替代不掉 |
| **🟡 P1** | Bash | **后台执行 + `Monitor` 流式跟踪** | 长任务跟踪能力，与调度面 P0 有部分耦合 |
| **🟡 P1** | 工作流 | **复合 review skill（`/code-review` 的 low/medium/high + `--fix`）** | 不含云端 `ultra` 模式；本地 plan-mode + propose 应能实现 |
| **🟠 P2** | MCP | **TUI 内重启 server** | 中等投入 |
| **🟠 P2** | 工具 | **`NotebookEdit`（Jupyter）** | 数据科学场景；用户群可能小 |
| **🟠 P2** | 工具 | **`WebSearch`** | 不只是 fetch 单 URL；Seek 已有 webfetch 框架可扩展 |
| **🔵 P3** | UX | **Fast 模式** | 模型/路由层打磨 |
| **🔵 P3** | 平台 | **Devcontainer 检测 / Docker 沙箱** | 与"单二进制零依赖"卖点张力较大 |
| **🔵 P3** | UI | **语音输入** | macOS 原生路径，中等投入 |
| — | 平台 | **SDK（TypeScript/Python）** | 超出范围（Seek 是工具而非框架） |
| — | 企业 | **SSO、用量分析、Managed MCP** | 超出范围（单用户工具） |
| — | 多端 | **Computer use / Chrome 扩展 / 桌面 / Web** | 与"CLI-first 单二进制"定位正交 |

### Seek 的独特优势（Claude Code 不具备）

| 功能 | 价值 |
|---|---|
| **多 LLM 提供商** | DeepSeek、Anthropic、OpenAI、Gemini、OpenAI 兼容端点——不绑定单一厂商 |
| **DeepSeek 单价 + 前缀缓存** | DeepSeek 基础单价已显著低于 Anthropic；叠加 95.7% 实测缓存命中后，同等任务量级下 token 成本远低于 Claude Code 默认链路 |
| **FIM 端点** | 通过 DeepSeek FIM 提供小范围原地编辑工具，比 chat 模式更便宜 |
| **错峰 5 折** | DeepSeek 错峰时段（北京 00:30–08:30）半价；Seek 状态栏内置计时器和自动费用计算 |
| **单二进制 ~5 MB** | 零运行时依赖，`go install` 或 `curl \| tar` 即用 |
| **`think` + `dual-model` skill** | reasoner→chat→reflect 推理闭环 |
| **三层记忆 + `-dream`** | `seek -dream` 做跨项目用户偏好归纳，decay-score GC 自动遗忘 |
| **零遥测** | 隐私优先，无数据收集 |
| **无地区限制** | 全球可用 |
| **MIT 协议** | 宽松许可，无使用限制 |

---

## 核心结论

1. **核心对等**：Seek 在 Agent 基础循环、Plan/权限、MCP、Skills 上与 Claude Code 对等；记忆方向一致但模型不同（Seek L/M/S vs. Claude Code 类型化条目）。这些是日常编码最常用的能力。

2. **最大差距集中在一处**：**多 agent + 调度 + worktree 隔离**这套组合（`Agent` / `CronCreate` / `ScheduleWakeup` / `EnterWorktree` / `RemoteTrigger` / `PushNotification`）在 Claude Code 里互相耦合，要做就得一起设计。Seek 目前是单进程单 agent。

3. **第二档单点工具缺口**：`LSP`、`Monitor` + 后台 bash、`NotebookEdit`、`WebSearch`、结构化 `AskUserQuestion` —— 都是独立可补的点，每个一两天工程，挑两个做能很快缩差距。

4. **v0.4.x 已补齐的差距**：自动 git checkpoint、文件级 undo/redo、shell hooks、斜杠命令 Tab 补全、可自定义快捷键。Workflow 层面已基本对齐 Claude Code。

5. **独特优势**：多 LLM 支持 + DeepSeek 单价/缓存/FIM/错峰组合，叠加单二进制与零遥测，这些是 Claude Code 体系内难以复制的差异化能力。

6. **明确边界**：企业管理（SSO、用量分析、Managed MCP）、SDK、桌面/Web/Chrome 扩展、Computer use、语音输入——都不是 Seek 的目标。Seek 定位为单用户本地 CLI 工具，而非平台 / 框架 / 多端产品。除非定位变化，否则不应作为差距追赶。
