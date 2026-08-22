# seek 竞品对比：Claude Code（能力天花板）+ Reasonix（同论题竞品）

> **最后更新**：2026-06（v6 五柱全交付 + v7 四柱全交付 + 新增用户指南 + book 第 23–24 章）
> **本文覆盖两个对比点，二者关系不同**：
> - **§1–核心结论：seek vs Claude Code** —— CC 是**能力天花板/参考标杆**，本节是**逐项 gap 追踪器**（✅/❌/🔶 + P1/P2 优先级），驱动 seek 的 roadmap。
> - **末节：seek vs Reasonix** —— Reasonix 是**同论题孪生竞品**（DeepSeek 原生 Go agent），本节是**差异化定位分析**（趋同/分歧 + 站位），驱动 pitch/messaging，不是 gap 追踪。
> **数据来源**：CC 侧 = 官方文档 + 直接观察；seek 侧 = 代码库主干（逐项回核）；Reasonix 侧 = README/仓库页 **+ `main-v2` 关键源码**（已读 coordinator/task/bgjobs/checkpoint/acp/serve）。

系统对比 **Seek**（开源、DeepSeek 优先、多 LLM 智能体）与 **Claude Code**（Anthropic 的自主编程智能体参考实现）。Reasonix 的对比见文末专节。

> **v0.6.x 重要变化**：v5 柱 G（v0.6.0 — 子代理 + worktree 隔离）和柱 H（v0.6.1 — cron / wakeup / triggers / OS notification）已 ship，关闭了过去版本里"架构级缺口"的全部 3 个 🔴 P0 项目。本次更新把 §7 / §10 / 差距汇总 / 核心结论同步到最新代码现实。

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
| LSP 工具（结构化符号/引用） | ✅ | 🔶 **等效替代** | — | v6 柱 L（瘦身版）：`references` 工具走 gopls/pyright/tsserver 拿**语义引用**（grep 替代不了的硬赢）。definition/hover/symbols **故意不做**——Go 里被 grep+`go build` 覆盖，ROI 低（见 `feature-lsp.md` §动机）。会话级 server、零 daemon |
| Notebook 编辑 | ✅ | ❌ **缺失** | — | Claude Code 有 `NotebookEdit` 原生编辑 Jupyter `.ipynb`；Seek 无 |
| 后台 bash + 流式监听 | ✅ | ✅ | — | 双方均支持 `run_in_background` + `monitor`（poll/wait/kill）跟踪后台 job。seek 为会话级（随会话生死、零 daemon），v6 柱 K |
| Web 搜索 | ✅ | ❌ **缺失** | — | Claude Code `WebSearch`；Seek 只有 `webfetch`（HTTPS GET 单页） |
| Fast 模式 | ✅ | ❌ **缺失** | — | Claude Code 有 `/fast` 模式，对简单任务快速低消耗响应 |
| Headless / 非交互模式 | ✅ | 🔶 **等效替代** | Seek 有 JSON-RPC 2.0 service 模式（`--rpc`）用于 IDE 接入和 `-json` / `-p` 一次性输出，但不是通用 `--headless` 标志 |
| 会话持久化 | ✅ | ✅ | **对等** | JSONL 格式（`schema_version=3`），自动保存，`/branch` 分支，`/compact` 压缩，断点续传 |
| 上下文窗口管理 | ✅ | ✅ | **对等** | `/compact` 压缩，DeepSeek 前缀缓存（实测 95.7% 命中率） |
| 交互式 / 流式 | ✅ | ✅ | **对等** | inline TUI（bubbletea），流式输出，`ask_user` TUI 选择器 |
| 自动继续 / steer | ✅ | ✅ | **对等** | Seek：`/steer` 中途插入指令，`AutoContinue` 模式 |
| 多行输入 | ✅ | ✅ | **对等** | Textarea 输入，路径自动补全 |
| 自治达标循环（`/goal`） | ✅ **v2.1.139** | ✅ | **对等，且 seek 更广** | CC `/goal` 单 agent 死磕到达标（Haiku 每轮判），仅 session 级。Seek `/goal`（feature-goal.md）：便宜模型每轮判 + **TUI + headless `seek goal run` + `cron create --goal`**——可定时无人值守跑、push 手机，CC 的 session 级 `/goal` 做不到 |

**小结**：核心 chat/edit 循环对等。原"四件套"差距已收敛：**`Monitor` + 后台 Bash**（柱 K）、**`LSP`**（柱 L `references`，语义引用部分）已补；CC 新增的 **`/goal`** 也已对齐（且 seek 可 headless/cron 组合，更广）；剩 **`NotebookEdit`**（notebook 编辑）、**`WebSearch`**（不止 fetch 单页）。Fast 模式属于 UX 便利功能而非架构级特性。

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
| Worktree 管理 | ✅ | ✅ **v0.6.0** | **对等** | Seek：`enter_worktree` / `exit_worktree` 工具 + `seek worktree list/gc` CLI + `/worktrees` TUI 面板；`exit if_dirty=discard` 把 work 暂存到 `refs/seek/discarded/<ts>` 防误删 |
| Tab 补全（TUI 命令） | ✅ | ✅ **v0.4.x (M9.5)** | **对等** | Seek：斜杠命令 Tab 补全；唯一匹配直接 accept，多匹配填到最长公共前缀 |
| Shell 命令 hooks | ✅ | ✅ **v0.4.1** | **对等** | Seek：`.seek/hooks.toml` 可配置 `pre_tool` deny + 五个 observer |

**小结**：开发工作流剩余差距收敛到 **PR 流程优化 prompt** 和 **云端 ultra review**。自动 git checkpoint、文件级 undo/redo 已在 v0.4.0 补齐，斜杠命令 Tab 补全在 v0.4.x（M9.5）补齐，worktree 管理在 v0.6.0 补齐（与下方 §10 子代理强耦合）。

---

## 8. UI/UX 与交互

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| Inline TUI 模式 | ✅ | ✅ | **对等** | 双方都用 tea.Println 写 scrollback + 实时渲染 |
| 状态栏 | ✅ | ✅ | **对等** | 模型、费用、缓存命中率、错峰倒计时 |
| `/` 斜杠命令 | ✅ | ✅ | **对等** | Seek 已覆盖 `/help` `/clear` `/model` `/effort` `/yolo` `/plan` `/review` `/branch` `/compact` `/distill` `/skill` `/skills` `/memory` `/steer` `/setup` `/upgrade` 等 16 个 |
| 用户提问选择器 | ✅ | ✅ **v6 柱 I v2** | **对等** | Seek v2：一次最多 4 题、每题 2–4 选项、multiSelect、preview 双栏渲染（mockup / 代码片段 / diagram）、自动 "Other" 自由文本。与 Claude Code `AskUserQuestion` 完全对等 |
| `help` 浮层 | ✅ | ✅ | **对等** | 双方都有可关闭的浮层 |
| 快捷键绑定（可自定义） | ✅ | ✅ **v0.4.x (M9.4)** | **对等** | Seek：`~/.seek/keybindings.toml` + `internal/keymap` / `internal/keyscli`（`seek keys check` 可校验） |
| 输出样式 | ✅ | ❌ **缺失** | — | Claude Code 有多种输出样式预设 |
| 语音输入 | ✅ (待确认) | ❌ **缺失** | — | macOS 终端层面的语音输入是 OS 能力，是否在 Claude Code 内有专门集成存疑 |
| Computer use（GUI 自动化） | ✅ (API 层) | ❌ **缺失** | — | Anthropic Computer Use 是 API 能力，在 Claude Code CLI 内的暴露形式未直接验证 |
| Chrome 扩展 | ✅ | ❌ **缺失** | — | 浏览器集成，获取 Web 上下文 |
| IDE 集成（VS Code / JetBrains） | ✅ | 🔶 **等效替代** | Seek 通过 `--rpc` JSON-RPC 2.0 服务暴露给编辑器；不是完整的 IDE 扩展 |
| 桌面应用 | ✅ | ❌ **缺失** | — | Claude Code Desktop（Electron） |
| Web 界面 | ✅ | ❌ **缺失** | — | Claude Code on the Web |

**小结**：核心 TUI 对等，快捷键自定义已在 v0.4.x（M9.4）补齐；用户选择器 v6 柱 I v2 后已与 Claude Code 对等（多题/多选项/preview）。语音、computer use、IDE 扩展、桌面/Web 等属于多端平台战略而非 Agent 核心。

---

## 9. LLM 与成本管理

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 多提供商支持 | ❌（仅 Anthropic） | ✅ | **Seek 优势** | DeepSeek、Anthropic、OpenAI、Gemini、OpenAI 兼容端点 |
| DeepSeek 专属能力 | ❌ | ✅ | **Seek 优势** | V4 推理模式、FIM 端点、错峰 5 折、前缀缓存追踪 |
| 费用追踪（每会话） | ✅ | ✅ | **对等** | Seek：累计 + 每轮费用，状态栏实时显示 |
| 缓存命中率显示 | ✅ | ✅ | **对等** | Seek：实时命中率 + 节省 token 数 |
| 错峰折扣倒计时 | ❌ | ✅ | **Seek 优势** | 状态栏显示 5 折时段（高峰 = 北京 09:00–12:00 / 14:00–18:00，其余全部错峰，2026-08-16 起） |
| 定价对比 | ❌ | ✅ | **Seek 优势** | README 中的多提供商定价矩阵 |

**小结**：LLM/成本方面 Seek 完胜——提供商自由 + DeepSeek 专属优化。Claude Code 绑定 Anthropic。

---

## 10. 高级 Agent 功能（多 agent / 调度 / 远程）

| 功能 | Claude Code | Seek | 状态 | 说明 |
|---|---|---|---|---|
| 子代理（`Agent` 工具，可指定 `subagent_type`） | ✅ | ✅ **v0.6.0** | **对等** | Seek：`agent` 工具，标 `ReadOnly` 让一轮多次 `agent` 调用**并行分派**；`permission.Policy.Spawn` 强制权限单调收紧；`cache.Tracker.AdoptChild` 把子 token 累加到父状态栏 |
| 子代理类型库（Explore / Plan / general-purpose 等） | ✅ | ✅ **v0.6.0** | **对等** | Seek：三个内置模板 `explore`（只读调研）/ `plan`（起方案 · 已窄化为只读）/ `general-purpose`（继承父 Policy），通过 `agent({type: ...})` 选择 |
| 定时任务 / Routines（`CronCreate` + `/schedule`）| ✅ | ✅ **v0.6.1** | 🔶 **架构差异** | Seek：`seek cron create/list/run/delete/tick`，**零常驻 daemon**——委派 OS 调度器（launchd / systemd / cron / Task Scheduler）每分钟调用 `seek cron tick`。Claude Code routines 是**云托管**远程 agent；seek 是**本地** agent，机器关机就不跑（trade-off：无 vendor lock-in + 无数据上传 ↔ 无"睡觉时跑"可靠性） |
| 模型自调用唤醒 | ✅ | ✅ **v0.6.1** | **对等** | Seek：`schedule_wakeup` LLM 工具——模型说"30 分钟后再来检查"自动注册 `max_runs=1` 一次性 cron 任务 |
| 远程触发 / PushNotification | ✅ | ✅ **v6 柱 M + v7 柱 N** | **对等** | Seek：文件桥触发 `~/.seek/cron/triggers/<id>.json`（CI / IDE 插件写文件、下次 tick 消费）+ OS 通知（macOS osascript / Linux notify-send / Windows toast）+ **webhook push 到手机**（ntfy/Slack/Discord/飞书/自定义，v6 柱 M）+ **Autopilot 完成推送**（v7 柱 N）。Claude Code 走云 push；seek 走 webhook 桥——用户自选渠道，等价覆盖"离开电脑也能收"的需求 |

**小结**：v5 柱 G（v0.6.0）+ 柱 H（v0.6.1）关闭了过去版本里全部 3 个 🔴 P0 架构级缺口。剩下的是**架构选择**而非缺口——Claude Code 的 routines 走云托管（vendor lock-in + 移动 push + 24/7 可靠 vs seek 的零 daemon + 本地隐私 + 机器关机就不跑）。两套模型针对的是不同用户画像，不再适合用"对等/缺失"二元判断。

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
| **🟢 已交付** | Agent | **子代理 / `agent` 工具 + 3 个内置 type** | v0.6.0（v5 柱 G）：`agent` 工具 + `explore/plan/general-purpose` 模板 + 父子权限单调收紧 + cost 累加 + `/agents` TUI 面板 |
| **🟢 已交付** | 工作流 | **Worktrees（`enter_worktree`/`exit_worktree`）** | v0.6.0：与子代理强耦合 + `seek worktree list/gc` CLI + `refs/seek/discarded/<ts>` 防误删 |
| **🟢 已交付** | 调度 | **Cron / Wakeup / Triggers / OS 通知** | v0.6.1（v5 柱 H）：`seek cron` CLI + `schedule_wakeup` LLM 工具 + 文件桥 trigger + macOS/Linux 通知。**零常驻 daemon**，架构上与 Claude Code 云托管 routines 不同（trade-off 见 §10 小结） |
| **🟢 已交付** | UI | **`AskUserQuestion` 结构化选择器 v2** | v0.7.0（v6 柱 I）：多题 stack（1–4 题/调用）+ `preview` 双栏渲染（mockup / diagram）+ `header` chip。polymorphic schema——v1 零破坏。完美对等 |
| **🟢 已交付** | 工具 | **LSP 语义引用（`references`）** | v0.7.0（v6 柱 L 瘦身版）：gopls/pyright/tsserver 语义引用（grep 替代不了的硬赢）。懒启动 + 会话级缓存 + crash 重启 + 缺 binary 降级 grep。definition/hover/symbols 故意不做（ROI 评估） |
| **🟢 已交付** | Bash | **后台执行 + `monitor` 跟踪（poll/wait/kill）** | v0.7.x（v6 柱 K）：`bash run_in_background` 返回 `bg-N` 句柄 + `monitor` 工具轮询/等待/杀；会话级生命周期、`until_regex` 条件等待、`Manager.Shutdown` 退出全杀。Windows 为降级路径 |
| **🟢 已交付** | 工作流 | **复合 review skill（`/code-review` 的 `quick`/`thorough` + `--fix` + `--comment`）** | v0.7.0（v6 柱 J）：内置 `code-review` skill（方法论 + effort framing）+ `/code-review` slash 命令（参数解析，复用 `/review` diff 采集 + picker）+ `/review` = `/code-review quick` 别名。**eval 实测 DeepSeek 分不开 4 档 → 收敛为 2 档**（旧 low/medium/high/max 软别名映射）。`--fix` 走 plan-mode propose；不含云端 `ultra`（架构选择） |
| **🟢 已交付** | 推送 | **移动端可达（webhook 桥）** | v0.7.0（v6 柱 M）：`push_webhooks` 把 cron/trigger 终态额外 POST 到用户自选渠道（ntfy/slack/discord/raw），离开电脑也能收。**故意不做** native 云 push（反零 daemon/隐私立场，见 §6 边界）——webhook 桥让用户自接渠道，等价覆盖「手机收得到」的需求。`seek cron config check --probe` 验证可达 |
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
| **错峰 5 折** | DeepSeek 错峰时段（高峰 = 北京 09:00–12:00 / 14:00–18:00，其余全部错峰）半价；Seek 状态栏内置计时器和自动费用计算 |
| **单二进制 ~5 MB** | 零运行时依赖，`go install` 或 `curl \| tar` 即用 |
| **`think` + `dual-model` skill** | reasoner→chat→reflect 推理闭环 |
| **三层记忆 + `-dream`** | `seek -dream` 做跨项目用户偏好归纳，decay-score GC 自动遗忘 |
| **零遥测** | 隐私优先，无数据收集 |
| **无地区限制** | 全球可用 |
| **MIT 协议** | 宽松许可，无使用限制 |
| **零常驻 daemon 的本地调度** | seek 不跑后台进程；定时任务委派 OS（launchd / systemd / Task Scheduler）拉起 `seek cron tick`。无 resident memory 占用、无"是否启动"歧义、无 auto-start config 维护、跨重启自动恢复 |
| **文件桥触发**（无需 webhook server） | CI / IDE 插件写 `~/.seek/cron/triggers/<id>.json`，下次 tick 消费。不需要 HTTP server / 端口管理 / 反向代理 / TLS / 认证 |
| **`~/.seek/cron/env` 显式注入** | 解决 OS 调度器 env 极简问题（launchd/systemd/cron 都不会继承 shell 的 `DEEPSEEK_API_KEY`）。dotenv 格式 + 解析错误明确报错（不会"静默掉 API key"） |

---

## 核心结论

1. **核心对等**：Seek 在 Agent 基础循环、Plan/权限、MCP、Skills 上与 Claude Code 对等；记忆方向一致但模型不同（Seek L/M/S vs. Claude Code 类型化条目）。这些是日常编码最常用的能力。

2. **过去的"最大架构缺口"已关闭（v0.6.x）**：子代理（v0.6.0）、worktree 隔离（v0.6.0）、cron / 唤醒 / 触发 / OS 通知（v0.6.1）三个 🔴 P0 项全部 ship。Seek 不再是"单进程单 agent"——模型可以 spawn 并行子代理（standalone session + token 账户 + 可选 worktree 隔离），可以让自己定时唤醒，可以由外部 CI/IDE 触发跑活。**剩下的"差距"全部是架构选择**而非缺口（云托管 routines vs 本地零 daemon），针对的是不同用户画像。

3. **第二档单点工具缺口（基本收敛）**：结构化 `AskUserQuestion`（柱 I）、复合 review（柱 J）、移动 push（柱 M）、`Monitor` + 后台 bash（柱 K）、`LSP` 语义引用（柱 L `references`）均已 ship；**剩 `NotebookEdit`、`WebSearch`**（+ LSP 的 definition/hover/symbols，经评估 ROI 低、故意不做）。v6 五柱全交付。

4. **v0.4–v0.6 已补齐的差距**：v0.4.x 补齐 Workflow 层（git checkpoint、undo/redo、hooks、Tab 补全、自定义键位）；v0.6.x 补齐 Agent 编排层（子代理、worktree、cron / wakeup / triggers）。三档全部对齐 Claude Code 后剩 P1/P2 的"单点"。

5. **独特优势 + 架构选择**：多 LLM 支持 + DeepSeek 单价/缓存/FIM/错峰 + 单二进制 + 零遥测 + **零常驻 daemon 的本地调度**（v0.6.1 新）+ **文件桥触发**（无需 webhook server）。其中零 daemon 是与 Claude Code 云模型最不一样的架构表达，trade-off 是失去"睡觉时也在跑"的可靠性。

6. **明确边界**：企业管理（SSO、用量分析、Managed MCP）、SDK、桌面/Web/Chrome 扩展、Computer use、语音输入——都不是 Seek 的目标。Seek 定位为单用户本地 CLI 工具，而非平台 / 框架 / 多端产品。除非定位变化，否则不应作为差距追赶。

7. **v7 四柱全交付**：Autopilot（无人值守编排）+ OS 沙箱（seatbelt/landlock）+ ACP（Zed 编辑器集成）+ 离线 OCR（图片→文字）——四柱已落地，详见 [`docs/prd/v7.md`](prd/v7.md)。新增 [`docs/guide-autopilot.md`](guide-autopilot.md)、[`docs/guide-sandbox.md`](guide-sandbox.md)、guide-ocr.md（后随柱 Q 下线移除，接替者 [guide-vision.md](guide-vision.md)）、[`docs/guide-webhooks.md`](guide-webhooks.md) 用户指南。本书新增第 23 章（v6 五柱）和第 24 章（v7 四柱）。

---

# 同论题竞品：seek vs Reasonix（定位对比）

> **这是一种不同类型的对比**。上面 §1–核心结论是"追赶 Claude Code 天花板"的 gap 追踪器。本节是"和同论题孪生竞品的差异化定位"——重点**不是** gap（重叠是常态），而是站位。
>
> **数据可信度**：Reasonix 侧 = [github.com/esengine/DeepSeek-Reasonix](https://github.com/esengine/DeepSeek-Reasonix) README/仓库页 **+ `main-v2` 关键源码**（coordinator / task / bgjobs / checkpoint / acp / serve 已读）。下表已据源码**校正**——README 印象里以为 Reasonix 没有的几项（后台任务 / checkpoint / IDE 集成）其实都有。
>
> **版本二重性**：Reasonix 正在重写中——`main`（TS 0.x，维护模式，DeepSeek-only）vs `main-v2`（Go 重写，活跃，多 OpenAI-compatible）。下面以 **Go v2（活跃线）** 为准。

## R.0 一句话

Reasonix ≈ **seek 的孪生**：DeepSeek 原生、Go 单二进制、prefix-cache 字节稳定、plan/skills/MCP/hooks/session 全有，**MIT、14.9k stars / 870 forks**（远大于 seek）。差异不在"是什么"，在"往哪长"。

## R.1 趋同（红海重叠——任何一项都不构成差异化）

DeepSeek 原生 + **prefix-cache 字节稳定**（seek 95-97% / Reasonix 99.82% 案例）· Go 单二进制 · 多 provider（v2）· Plan 模式 · Skills（都兼容 Markdown/Agent-Skills）· MCP client · Hooks · 权限白名单 · 按目录持久化 session · headless 一次性执行 · 终端优先 · 本地 · BYO key · 开源。

→ **"DeepSeek 省钱 + 前缀缓存 + 终端 Go agent"在二者之间是 0 分差异。**

## R.2 分歧矩阵

> 下表为**读源码后的校正版**。⚠️ 标记 = README 印象被源码推翻处。

| 维度 | seek | Reasonix（main-v2 源码） | 谁强 |
|---|---|---|---|
| **子代理：并行+隔离** | spawn 子代理 **并行** + **git worktree 隔离** + 权限单调收紧 + 成本归集 | `TaskTool` 子代理（独立 session、sync/async 跨 turn），但**源码注释明确故意串行、无 worktree**（"keeps the parallel-dispatch path from running two sub-agents at once…writes race"） | **seek**（仅"并行+隔离"这一点；"有没有子代理"是对等） |
| **时序自治** | cron + wakeup + 文件触发 + OS 通知 + **手机 webhook push**（零 daemon） | **源码零调度**：无 cron/wakeup/trigger/schedule 任何文件 | **seek（干净领先）** |
| ⚠️ **后台任务** | `bash run_in_background` + `monitor`（poll/wait/kill, until_regex） | **有**：`bash/task run_in_background` + `bash_output`/`wait`/`kill_shell`（session 级，无 list） | **≈ 对等**（原以为 seek 独有，错） |
| ⚠️ **编辑安全网** | 每 turn **git** snapshot + 文件级 undo/redo/restore（git stash 连 bash 副作用一起盖） | **有** checkpoint：**git-free** turn-rewind（"like Claude Code rewind"，仅 edit-tool、不含 bash） | **≈ 对等**（实现取舍不同；原以为 seek 独有，错） |
| ⚠️ **IDE 集成** | JSON-RPC `-rpc` + **ACP**（`seek acp`，柱 P 已交付）：initialize/session.new/prompt/cancel/update，真 server stdio e2e | **ACP**（Agent Client Protocol）：session/load/prompt + 事件流 + 审批路由，e2e 测过——**Zed 等用的标准协议** | **≈ 对等**（seek v0.8 已补齐 ACP；原以为 seek 只有自定义 RPC，已更新） |
| **OS 沙箱** | ✅ **macOS seatbelt + Linux landlock**（柱 O 已交付）：零 runtime 依赖、单二进制。seatbelt SBPL confine 写+网络；landlock trampoline re-exec 实现，fail-closed。子代理 per-worktree 集成 autopilot | **有** macOS Seatbelt 沙箱（`sandbox/seatbelt_darwin`） | **≈ 对等**（seek 双平台、零依赖；Reasonix 仅 macOS；原以为 seek 无沙箱，错） |
| **Web 前端** | ❌ 仅 TUI + 状态栏 | **生产级** web 前端（SSE 事件流 + REST submit/cancel/approve/plan/compact + context/token 指标；源码非 stub） | **Reasonix** |
| **语义索引** | ❌ 故意不做（grep+read+`references`） | **有** `codegraph`（含 install + e2e） | Reasonix（路线分歧） |
| **桌面 / 远程** | webhook push（单向通知） | Tauri 桌面（预发布）+ QQ 双向远程 | Reasonix |
| **Web search** | webfetch 单页（WebSearch 在 P2） | 多 provider（Bing/Baidu/SearXNG/Tavily/Perplexity） | Reasonix |
| **Memory 模型** | L/M/S 三层 + `seek -dream` 跨项目蒸馏 + decay GC | 四分类（user/feedback/project/reference） | 路线不同 |
| **DeepSeek 深度** | FIM 端点 + 错峰 5 折倒计时 + V4 `think` | billing/balance（**未见** FIM/错峰） | seek 略深 |
| **规模/心智份额** | 新/小 | **14.9k stars / 870 forks** | **Reasonix（碾压）** |

## R.3 各自赢的（源码校正后）

- **seek 干净领先的仍有两轴**：(1) 子代理**并行 + git worktree 隔离**（Reasonix 子代理故意串行、无隔离）；(2) **时序自治**（cron/wakeup/触发/手机 push；Reasonix 源码零调度）。外加 **Autopilot 无人值守编排**（柱 N，seek 独有）+ DeepSeek FIM/错峰的边角深度。
- **已追平**：ACP 编辑器集成（柱 P，seek 现已支持）· OS 沙箱（柱 O，seek 双平台 macOS seatbelt + Linux landlock，零依赖）。
- **Reasonix 仍赢**：规模碾压（14.9k、435M token 战测）· `codegraph` 语义索引 · 生产级 web 前端 · 多 provider 搜索 · QQ 双向远程。
- **大面积对等（读源码后比 README 印象更趋同）**：后台任务、编辑安全网/rewind、plan、skills、memory、MCP、hooks、权限、session、headless——这些都不是差异点。

## R.4 对 seek 的站位结论（读源码后更尖锐）

1. **结论已修复**：上一版低估了 seek——seek 的 Autopilot（柱 N）是 Reasonix 没有的编排层；柱 P ACP 补齐了 IDE 集成缺口；柱 O 沙箱追平了 macOS 且多了一个 Linux landlock。seek 真正的领先轴已是三轴：**并行+worktree 子代理、时序自治+Autopilot 无人值守、零依赖双平台沙箱**。详见 [`docs/guide-autopilot.md`](guide-autopilot.md)、[`docs/guide-sandbox.md`](guide-sandbox.md)。
2. **pitch 应压这三轴**，且认可 Reasonix 在规模/语义索引/web 前端上的优势。ACP 和沙箱不再是差异点。
3. **别在重叠区硬碰**——DeepSeek/cache/Go-binary 又大又先发，主打它 = 给对手做嫁衣。
4. **"无语义索引"是主动下的注**（柱 L 选 grep+references 轻路线 vs Reasonix 押 `codegraph` embeddings）——pitch 要能**主动解释为什么不做**（零索引常驻 / 本地轻量），否则被当短板。
5. **一句话区隔（v0.8 更新）**：*"两者都是 DeepSeek-native Go agent。Reasonix 更全面（语义索引 / web 前端 / 多搜索 / 远程桌面）且大得多。seek 独有的差异是**把单 agent 变成并行隔离的团队、无人值守自动编排（Autopilot），并在你睡觉时定时/触发交付、push 到手机**——ACP 和沙箱已追平，其余基本对等。"*

> **已坐实（读 `main-v2` 源码）**：coordinator / task / bgjobs / checkpoint / acp/service / serve 均已读。Reasonix **确有**：子代理（`TaskTool`，串行无隔离）、后台任务、checkpoint+rewind、ACP 编辑器集成、生产级 web 前端、`codegraph` 语义索引、macOS 沙箱。**源码确无**：调度/cron/wakeup/trigger、worktree 隔离、并行子代理 fleet。未深读：`codegraph` 内部算法、billing——但存在性已确认。
