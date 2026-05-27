# Feature: 用户可配置的 Shell Hooks

**所属版本**：v3（柱 B · 可扩展性）
**前置阅读**：[PRD v3 umbrella](v3.md) §3（跨柱约束）、[`internal/hooks/hooks.go`](../../internal/hooks/hooks.go)（已有的 typed hook Registry）
**状态**：🚀 已交付（v0.4.1）
**实现提交**：`66a4312`（M9.2 hooks 核心 + 12 验收测试） → `50a7f01`（M9.3 stdin trust 提示 + audit log）
**验收**：12/12 验收标准全部通过，`go test -race` 全绿

---

## 1. 动机

seek 内部已有结构良好的 hooks Registry（`PrePromptHook` / `PreToolUseHook` / 五个 Observer），但它**只对 Go 代码开放**——v1 memory 子系统、v2 skill stats 都通过这个 Registry 接入，但外部用户用不到。

Claude Code 用户可以写一行 shell 命令在工具调用前/后跑 lint、记 audit log、阻拦 `kubectl delete` 之类的危险动作；seek 用户做不到，只能改源码重编译。

本 PRD **不发明新的拦截路径**——而是把"用户可配置的 shell 命令"作为 `internal/hooks` Registry 的一种实现（`ShellRunner`），注册成 `PreToolUseHook` + 五个 Observer。这样：

- prefix-cache 的字节级确定性约束自动继承（hooks 的合约已经写明）
- v1 memory / v2 skill stats 的复用模式直接对齐
- 未来如果还要加新事件，只需要扩 Registry 一处

## 2. 设计目标与不做什么

### 目标

1. **复用已有 Registry**——不开新拦截路径；ShellRunner 是 Registry 的一种实现。
2. **prefix-cache 字节级确定性零妥协**——shell hooks 的 stdout **不**进 prompt（详见 §3.2）；只能 deny 或 observe。
3. **"信任已配置"而非"信任每次执行"**——hooks.toml 写入是显式动作，等同于授信；执行时**不再走 permission picker 询问**（否则 audit hook 失去意义）。
4. **trust 询问只针对项目级 hooks**——按 sha256 跟踪 hooks.toml 改动，借鉴 VS Code workspace trust。
5. **配置语义跟 seek 其他配置文件保持一致**——toml 格式，跟 `.seek/config.json` 之外的其他配置统一。
6. **失败显式化**——hook 超时、shell 语法错误、deny 都通过状态栏 + audit log 可见。

### 不做什么（v3 明确延后）

- ❌ **Hooks 不能改 prompt / args 字节**——v3 的 shell hooks 只能 **deny**（非零退出 → 拒绝该工具调用）或 **observe**。**不暴露**"改写工具参数"或"注入 prompt 前缀"——前者破坏 prefix-cache 确定性，后者已被 v1 memory 系统占用（`PrePromptHook` 只暴露给 Go 内部）。
- ❌ **不支持 yaml/jsonnet**——只有 toml。
- ❌ **不内嵌 lua/wasm 等脚本引擎**——执行成本（lua VM ~ 1MB；wasm runtime 数十 MB）跟 seek 单二进制定位冲突。`bash -c` 在三平台都可用，出问题用户能直接复现。
- ❌ **不接入 Claude Code 的 `claude-hooks.toml`**——格式相似但语义不同（事件名、env var、deny 协议各有差异），强行兼容会拖累两边。文档给迁移指引即可。
- ❌ **不支持 `tool_args_match`（参数 glob 匹配）**——v3 只匹配工具名；参数级匹配留给 v4。
- ❌ **不为 hook 提供沙箱**——hooks.toml 是用户/项目主动写入的，等同 shell rc；seek 不为内容做任何 sandbox。trust 询问是唯一防线。

## 3. 设计

### 3.1 配置文件结构

**两份配置叠加**：

```
~/.seek/hooks.toml              # 用户级，本机所有项目可见
<project>/.seek/hooks.toml      # 项目级，进 git，团队共享
```

合并规则：项目级先执行，用户级后执行（项目优先级高但**不替换**用户级）。两边都注册了同一事件时，按声明顺序串行执行；任意一个 `pre_tool` 拒绝则短路。

**Schema 示例**：

```toml
# ~/.seek/hooks.toml

[[pre_tool]]
name        = "block dangerous bash"
match       = { tool = "bash" }
command     = "scripts/check-bash.sh"
timeout_ms  = 2000

[[pre_tool]]
name        = "audit log"
match       = { tool = "*" }                       # 通配
command     = """echo "$(date -u +%FT%TZ) $SEEK_TOOL_NAME $SEEK_SESSION_ID" >> ~/.seek/audit.log"""

[[post_tool]]
name        = "lint after edit"
match       = { tool = "edit" }
command     = "make lint"
timeout_ms  = 30000

[[session_start]]
name        = "warm up cache"
command     = "scripts/warmup.sh"

[[session_end]]
name        = "upload usage"
command     = "scripts/report-usage.sh"
```

**字段定义**：

| 字段 | 必需 | 说明 |
|---|---|---|
| `name` | ✅ | 人类可读名字，用于状态栏 + audit log 显示 |
| `command` | ✅ | shell 命令字符串；通过 `bash -c <command>` 执行 |
| `match.tool` | ⬜（仅 pre_tool/post_tool）| 工具名 glob；`"*"` 匹配所有；缺省 = `"*"` |
| `timeout_ms` | ⬜ | 超时毫秒，默认 5000 |

### 3.2 事件目录

| 事件 | 接 Go 侧的接口 | 返回值语义 | 可 deny? |
|---|---|---|---|
| `pre_tool` | `PreToolUseHook` | 退出码 ≠ 0 → deny，stdout 进 deny reason；退出码 = 0 → allow | ✅ |
| `post_tool` | `PostToolUseObserver` | 无返回（退出码 / stdout 仅记入 audit log） | ❌ |
| `pre_prompt` | `PreTurnObserver`（**不**接 `PrePromptHook`，避免改 prompt） | 无返回 | ❌ |
| `session_start` | `SessionStartObserver` | 无返回 | ❌ |
| `session_end` | `SessionEndObserver` | 无返回 | ❌ |

**为什么 `pre_prompt` 不让改 prompt**：`PrePromptHook` 的合约是"deterministic output"——v1 memory 子系统通过它注入跨会话上下文，依赖输出在相同输入下完全一致，否则 prefix-cache 会以静默方式坍塌（命中率从 95.7% 跌到个位数）。外部 shell 命令的 stdout 不在 seek 的控制范围内（系统时钟、PID、网络结果），不能让用户在不知情时把 cache 命中率砸了。需要"在 prompt 里加东西"的场景请走 v1 memory 系统或 v2 skill 注入。

### 3.3 环境变量契约

Pre/Post 都注入，缺失字段为空字符串：

| Env var | 内容 | 哪些事件填 |
|---|---|---|
| `SEEK_VERSION` | 当前 seek 版本 | 全部 |
| `SEEK_SESSION_ID` | 当前 session ID | 全部 |
| `SEEK_PROJECT_ID` | v1 memory 计算的 16 字符项目 ID | 全部 |
| `SEEK_PROJECT_PATH` | 项目绝对路径 | 全部 |
| `SEEK_EVENT` | 事件名（`pre_tool` / `post_tool` / ...）| 全部 |
| `SEEK_TOOL_NAME` | 工具名 | `pre_tool` / `post_tool` |
| `SEEK_TOOL_ARGS_JSON` | 工具参数 JSON（紧凑、单行）| `pre_tool` / `post_tool` |
| `SEEK_TOOL_RESULT` | 最多 4096 字节，截断时附 ` [truncated]` | `post_tool` |
| `SEEK_TOOL_EXIT_OK` | `"1"` 成功 / `"0"` 失败 | `post_tool` |

### 3.4 执行模型

- 命令：`exec.CommandContext(ctx, "bash", "-c", command)`
- 工作目录：`SEEK_PROJECT_PATH`
- stdin：空（hooks 不接交互输入）
- 超时：`timeout_ms` 默认 5000ms
  - `pre_tool` 超时 → 按 deny 处理 + 状态栏报错
  - 其他事件超时 → observer 异常（写 audit log）
- 并发：同一事件的多个 hook **串行**执行（因为 deny 短路依赖顺序）；不同事件并发
- 静态检查：注册时 `bash -n` 检查语法；失败的 hook 跳过 + 启动横幅打印

### 3.5 trust 询问（项目级 hooks 的事前防线）

**触发**：第一次发现项目 `.seek/hooks.toml` 时——

```
该项目定义了 3 个 hooks：
  pre_tool  "block dangerous bash"
  post_tool "lint after edit"
  session_end "upload usage"

[详情] 查看文件内容
[y]    允许并记住
[n]    拒绝（hooks 不生效）
```

- 选 `y` → 把 `(project_path, sha256(hooks.toml))` 记入 `~/.seek/trusted-projects.json`
- 选 `n` → 项目级 hooks 全部 no-op 直到下次询问
- 修改后（sha256 变化）→ 重新询问

**为什么用户级不询问**：`~/.seek/hooks.toml` 是用户自己写的，没有第三方信任风险。改文件等同于改 `~/.bashrc`，不需要额外确认。

**为什么项目级要询问**：项目级 hooks 进 git，git clone 一个新项目时可能 inadvertently 触发恶意 hook（典型攻击面：`make` / `npm install` 类供应链）。借鉴 VS Code workspace trust 模型。

### 3.6 audit log

每次 hook 执行写一行到 `~/.seek/hooks-audit.jsonl`（append-only）：

```json
{
  "ts": "2026-06-15T10:24:13Z",
  "event": "pre_tool",
  "hook": "block dangerous bash",
  "tool": "bash",
  "session_id": "20260615-...",
  "duration_ms": 142,
  "exit_code": 0,
  "denied": false
}
```

**用途**：

- `seek hooks list` 显示每个 hook 的"近 50 次平均耗时"——用户能看到自己的 hook 拖慢了多少
- 调试 `pre_tool` deny 时的事件追溯
- 第三方监控工具可 tail 这个文件

**轮转**：v3 不轮转；10K 条 ≈ 1MB，用户几年才用到这个量。

## 4. CLI 与 TUI 命令

### 4.1 CLI

```
seek hooks list
    打印两份 hooks.toml 合并后的有效配置，标注每条来自 user 还是 project。
    附加列：近 50 次平均耗时（从 audit log 取）。

seek hooks check [--event <evt>] [--tool <name>]
    干跑模拟：给定事件 + 工具名，打印会执行哪些 hook（不实际执行）。
    用例：seek hooks check --event pre_tool --tool bash

seek hooks trust [--reset]
    重置项目级 hooks 的 trust 记录（解除信任或强制重新询问）。

seek hooks audit [--since <duration>] [--tool <name>] [--denied]
    查询 audit log；--denied 只看被 deny 的事件。
```

### 4.2 TUI slash 命令

```
/hooks                              等价于 seek hooks list（表格输出）
```

trust 询问在 TUI 内通过现有 picker UI 弹出，CLI 模式下用 stdin 交互。

## 5. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/hooks` | **新增** `ShellRunner` 类型，实现 `PreToolUseHook` + 五个 Observer 接口；注册进 Registry | 中 |
| `internal/hooksconfig`（新包） | hooks.toml 解析、合并、trust 询问；audit log 写入 | 中 |
| `internal/paths` | 新增 `UserHooksToml()` / `ProjectHooksToml()` / `TrustedProjectsJSON()` / `HooksAuditLog()` | 极小 |
| `internal/tui/commands.go` | 注册 `/hooks` slash 命令 | 小 |
| `cmd/seek` | 新增 `hooks` 子命令；启动时初始化 `ShellRunner` 注册到 Registry | 中 |
| `pkg/agent` | **不动**——通过 Registry 接入 | 0 |
| Session 文件格式 | **不变** | 0 |

### 5.1 跟 v1 memory / v2 skill 的兼容

- **shell hooks 看不到 memory 注入**——`PrePromptHook` 是 Go 内部接口，shell hook 只接 `PreTurnObserver`，看到的是已经注入完 memory 的 `History`。
- **Skill 安装的 sub-process 不触发 hooks**——v2 skill install 跑 git clone 是直接 `os/exec`，不经过 bash 工具；hooks 只看 agent 层的工具调用。

### 5.2 Prefix cache 影响审计

| 机制 | 是否进 prompt 字节序列 | 是否影响 cache |
|---|---|---|
| Shell hook `pre_tool` deny | 是（deny reason 作为 tool result 进历史）——跟现有 `permission.ErrDenied` 流程一致 | 同 permission 拒绝：拒绝那一刻 cache 命中等同 permission 拒绝，下一 turn 重新对齐 |
| Shell hook `post_tool` observe | 否（不进历史） | 否 |
| Audit log 写入 | 否 | 否 |

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | shell hook `pre_tool` 返回非零退出码时，工具调用被拒绝且 stdout 进 tool result 反馈给模型 | 单元测试（fake shell command） |
| 2 | shell hook `post_tool` 的 stdout/stderr **不**进 prompt 历史 | 单元测试 |
| 3 | 项目级 hooks.toml 首次出现时 TUI 弹 trust 询问；记入 trusted-projects.json 后不再询问 | 集成测试 |
| 4 | 修改项目级 hooks.toml（sha256 变化）触发重新询问 | 单元测试 |
| 5 | hook 超时（`timeout_ms` 内未返回）按 deny 处理且释放进程 | 单元测试（用 `sleep` 模拟） |
| 6 | hooks.toml 里有 bash 语法错的 hook → 注册时跳过 + 启动横幅打印；其他 hook 正常工作 | 单元测试 |
| 7 | `seek hooks check --event pre_tool --tool bash` 列出会执行的 hook（按合并后顺序）且不真执行命令 | 集成测试 |
| 8 | audit log append 并发安全（多 session 同时写不撕裂行） | 单元测试 + `-race` |
| 9 | 同一事件多个 hook 串行执行；第一个 deny 后第二个不被调用 | 单元测试 |
| 10 | `match.tool = "edit"` 时 bash 调用不触发该 hook；`"*"` 通配时所有工具都触发 | 单元测试 |
| 11 | env vars `SEEK_TOOL_NAME` 等正确注入；缺失字段为空字符串而非 unset | 单元测试 |
| 12 | hooks 注册不影响现有 v1 memory / v2 skill stats 行为 | 现有测试 |

## 7. 实现计划

| 子 ms | 内容 | 估时 |
|---|---|---|
| **M9.2** | `internal/hooksconfig` + `internal/hooks` 的 ShellRunner：toml 解析、合并、env 注入、超时；只接 `pre_tool` + `post_tool`；`seek hooks list/check` CLI | ~3 天 |
| **M9.3** | 剩余事件（session_start/end、pre_prompt 观察）+ trust 询问 + audit log + `seek hooks trust/audit` | ~1.5 天 |

**发版策略**：M9.2 + M9.3 合并为 **v0.4.1**——hooks 是团队场景的招牌功能，单独 ship 给企业/团队用户。

**与其他 feature 的并行关系**：

- 与 [`feature-checkpoint.md`](feature-checkpoint.md) 完全独立——前者改 permission/write/edit，后者只挂 Registry
- 与 [`feature-tui-ergonomics.md`](feature-tui-ergonomics.md) 仅 `cmdMeta` 注册条目层面有交集，无冲突

## 8. 风险

| 风险 | 缓解 |
|---|---|
| Shell hook 命令里有 `rm -rf $SEEK_PROJECT_PATH` 之类危险动作 | hooks.toml 是用户/项目主动写入，等同 shell rc——seek 不为内容做任何 sandbox。trust 询问是唯一防线，文档明示。 |
| Shell hook 拖慢每次工具调用 | `timeout_ms` 默认 5s；状态栏在 hook 占用超过 200ms 时显示耗时；`seek hooks list` 输出每条 hook 的"近 50 次平均耗时" |
| 用户在 hooks.toml 里写出 bash 语法错 | 注册时 `bash -n` 静态检查；失败时跳过该 hook + 启动横幅打印 |
| 项目级 hooks 在不同操作系统行为不一致 | 文档建议项目级 hooks 用 POSIX 子集或脚本写到 `scripts/` 子目录；hooks.toml 里只 `bash scripts/check.sh` 调用，避免 inline shell 跨平台坑 |
| 恶意项目 hooks.toml 在 trust 询问前已经被启动逻辑解析（如果解析有漏洞）| toml 解析只走 `BurntSushi/toml`，无 eval；trust 询问之前**不会**调用任何 `bash -c` |
| 用户 trust 了一个项目，后续 PR 引入恶意 hook 修改 | sha256 变化重新询问；文档建议团队 review hooks.toml 改动 |
| audit log 长大成 GB | v3 不轮转，但 10K 条 ≈ 1MB；v4 加轮转 |
| `bash -c` 在 Windows 没有原生 bash | 启动检测：缺 `bash` 时 hooks 全部 no-op + 打印 warning；建议 Windows 用户走 WSL / git-bash |

## 9. 后续版本

- **v4**：参数级匹配（`match.args_glob = "rm -rf*"`）
- **v4**：Hook 链可声明 `depends_on`（拓扑排序而非声明顺序）
- **v4**：audit log 轮转策略 + 压缩归档
- **v5**：远端注册的 hooks（公司中心化策略下发）——配合企业认证
