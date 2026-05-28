# Feature: Inspect RPC + Web 面板

**所属版本**：v0.6.x dot release（M12.0 并行于 v5；M12.1/M12.2 依赖 v5 数据）
**前置阅读**：[PRD v0](v0.md) §4.5（JSON-RPC 服务模式）、[PRD v5 umbrella](v5.md) §2.5（零常驻进程约束）、[`internal/rpc/server.go`](../../internal/rpc/server.go)（已有 method 集）
**状态**：📐 设计稿
**目标里程碑**：M12.0 + M12.1 + M12.2
**目标发版**：M12.0 → v0.6.x dot；M12.1/M12.2 → v0.7.0

---

## 1. 动机

seek 长期路线图（用户口述）：**未来要做应用**——桌面 / 移动 / Web 形态的前端，作为 CLI 的补充消费层。当前用户与 seek 的对话是 TUI 独占，所有内部状态（sessions / memory / hooks audit / 未来 subagent 树 / worktree 列表 / checkpoint 历史）只能通过文件系统 + `cat`+`jq` 触达，根本不具备喂应用的形态。

**应用阶段最大的风险不是 UI 做不做得出来，是数据契约定不准**——如果 v6/v7 应用项目启动后才发现"seek 没有 list-memory 的 stable method"，要么应用阶段返工拉一年节奏，要么把契约硬塞进半成品 API 形成长期负债。

本 PRD 通过**前置数据平面**化解风险：

- **复用已有 `--rpc` JSON-RPC 2.0** 服务面而不是新建 HTTP API—— seek 已为 IDE 集成投入了 `internal/rpc/server.go`，应用是同一份契约的另一类消费者
- **加 HTTP+SSE transport** 让浏览器/Tauri/Electron 这类前端能消费同一份 method 集（stdio transport 不变，IDE 用户继续受益）
- **Read-only Web 面板**作为 transport + method 集的第一个真实消费者，跑通端到端、把契约毛刺暴露出来；同时给 seek 自身贡献者一个可视化调试入口
- **写操作完全延后到应用阶段**——MVP 只查不改，避免在面板上做并发写控制等大量与"调试可视化"无关的工程

## 2. 设计目标与不做什么

### 目标

1. **方法名即契约**——一旦 ship，破坏性 rename 需 minor 版本 bump 和 deprecation 通道；新增方法是非破坏。`agent/info` 响应内带 `methods` 清单 + 版本号，应用阶段做 capability negotiation 用。
2. **transport 与 method 解耦**——所有 method 在 transport-agnostic 的 registry 里注册一次，stdio / HTTP 共享同一份 dispatch。新加 method 同时供两条 transport 用。
3. **零常驻 daemon**（继承 v5 §2.5）—— `--rpc-listen` 只在用户显式启动时存在，进程退出即停；与 TUI 同一进程生命周期。
4. **Web 面板不进主 binary**——静态资源单独发布 tarball；用户解压后浏览器打开 `index.html`，通过 fetch+SSE 连本地 `seek --rpc-listen`。保住 "5 MB 单二进制" 卖点。
5. **本地优先 + 默认安全**——HTTP transport 默认 bind 127.0.0.1；非 localhost 监听需显式 `--rpc-bind 0.0.0.0` 且自动启用 token 鉴权。
6. **失败降级**——session 文件被主进程独占写时 RPC 仍能读（RW 锁或快照读）；hooks observer SSE 客户端断开不影响 hooks 自身。

### 不做什么（v0.6.x dot / v0.7.0 明确延后）

- ❌ **写操作 method**——`session/edit` / `memory/add` / `agent/cancel` 等 mutating method 延后到应用阶段。本期 method 集**全部** read-only。
- ❌ **跨进程多 seek 状态同步**——面板只看连上的那个 `--rpc-listen` 进程；不做"看所有 seek 进程"的聚合。
- ❌ **认证 / 多用户**——本地工具，单用户假设。token 鉴权仅作非 localhost bind 的最低门槛，不是会话/权限系统。
- ❌ **Web 面板的 SPA 框架（React / Vue / Svelte）**——纯 vanilla JS + 少量 HTML + CSS；构建产物 < 500 KB 解压后。不引入 npm 工具链。
- ❌ **持久订阅 / 跨重启状态**——SSE 连接 seek 进程退出即断；客户端负责重连，不做 server-side replay。
- ❌ **嵌入式 panel 二进制**（v0.6.x 不做；v0.7.0 评估）——保住 CLI 单二进制；用户想要"一个 binary 跑全部"可走 `seek --rpc-listen --panel`（build tag `panel`，opt-in 编译）作为后续选项。

## 3. 数据模型与签名

### 3.1 Method 集（read-only 查询动词）

**M12.0 基础集**（与 v5 工作独立可 ship）：

| Method | params | result | 说明 |
|---|---|---|---|
| `agent/info` | — | `{version, model, yolo, plan, methods: [...]}` | 扩展现有：`methods` 列表 + `panel_protocol_version` |
| `session/list` | `{include_subagents?: bool}` | `[{sid, title, started_at, last_at, turns, model}]` | 扩展现有：`include_subagents` flag |
| `session/get` | `{sid}` | `{header, turns, cumulative_usage}` | header + 元数据；不含完整 messages |
| `session/messages` | `{sid, cursor?: string, limit?: int=50}` | `{messages: [...], next_cursor?: string}` | cursor 分页（基于 line offset），稳定于追加 |
| `memory/list` | `{project_id?: string}` | `[{name, kind, mtime, tagline}]` | project_id 缺省 = 当前项目 |
| `memory/get` | `{project_id?, name}` | `{name, kind, body, mtime, links: [...]}` | 完整 markdown body |
| `memory/projects` | — | `[{project_id, root, last_active_at}]` | 跨项目 memory 入口枚举 |
| `project/list` | — | `[{project_id, root, sessions: int, memories: int}]` | 已知项目枚举（基于 `~/.seek/projects/`） |
| `project/get` | `{project_id}` | `{root, agents_md_path?, hooks_toml_path?, ...}` | 单项目元数据 |
| `hooks/list` | `{project_id?}` | `[{name, event, command, source}]` | 当前已配置的 hooks（解析 hooks.toml） |
| `hooks/audit/tail` | `{from_ts?, follow?: bool, project_id?}` | `[{ts, event, tool, ...}]` *或*流式 | 非 follow → 一次返回；follow=true → SSE 通知 `hooks/event` |
| `stats/cache` | `{sid?}` | `cache.Tracker.Cumulative() 全字段` | sid 缺省 = 当前 session |

**M12.1 子代理集**（依赖 v5 M11.0 ship）：

| Method | params | result |
|---|---|---|
| `subagent/list` | `{parent_sid?, status?: "active"\|"completed"\|"failed"\|"all"}` | `[{sub_sid, parent_sid, type, status, description, ...}]`（折叠后的 §3.4 事件源状态） |
| `subagent/get` | `{sub_sid}` | `{sub_sid, parent_sid, type, status, description, worktree_path?, tokens, ts}` |
| `subagent/tree` | `{root_sid}` | 父-子嵌套树结构（v5 限制深度 1，但保留树形 schema 给 v6 嵌套） |

**M12.2 编排状态集**（依赖 v5 M11.1 + M11.2 ship）：

| Method | params | result |
|---|---|---|
| `worktree/list` | `{project_id?}` | `[{sub_sid, path, branch, changes, status}]` |
| `checkpoint/list` | `{sid?}` | `[{turn, ts, label, ref}]` |
| `cron/list` | — | `[{name, schedule, next_run_at, last_run_at, last_status}]`（柱 H 依赖） |

### 3.2 Method registry 重构

当前 `internal/rpc/server.go` 用 `switch case` 派发——加 method 几乎不可避免要碰主循环。M12.0 第一步是**抽出 registry**：

```go
// Method is one RPC method handler. Implementations should be pure
// (or read-only side-effecting at most — never mutate state via RPC
// in v0.6.x / v0.7.0).
type Method func(ctx context.Context, params json.RawMessage) (any, error)

// Registry holds the method dispatch table. Construct once at server
// start; safe for concurrent reads after RegisterFromXXX completes.
type Registry struct {
    methods map[string]Method
}

func NewRegistry() *Registry
func (r *Registry) Register(name string, m Method)
func (r *Registry) Dispatch(ctx context.Context, name string, params json.RawMessage) (any, error)
```

`Server.handleCall` 改为 `s.registry.Dispatch(...)`；现有三个 method (`agent/prompt` / `agent/info` / `session/list`) 通过 wrapper 注册进去保持向后兼容。

**契约稳定性约束**：

- Method 名字一旦进入 `agent/info` 响应的 `methods` 列表就**不能改名**——破坏性 rename 走 deprecation：先双名 + 旧名打 deprecation flag，至少一个 minor 版本后下线
- result 字段**新增**永远 OK，**删除/重命名**需 minor 版本 bump
- params 字段**新增可选**永远 OK，**新增必填**需 minor 版本 bump

### 3.3 Transport：stdio（保留）+ HTTP+SSE（新增）

**stdio**（现有）：保持不动，每行一个 JSON-RPC 对象，已有 IDE 集成用户零回归。

**HTTP+SSE**（M12.0 新增）：

```
seek --rpc-listen 127.0.0.1:0          # 自动选端口，stdout 印 `rpc listening on http://127.0.0.1:54321`
seek --rpc-listen 127.0.0.1:8080       # 指定端口
seek --rpc-bind 0.0.0.0 --rpc-token X  # 非 localhost 显式开启 + 必须 token（fail-closed）
```

HTTP 端点结构：

```
POST /rpc                     单次 method 调用，body = JSON-RPC 2.0 envelope
GET  /sse?topics=hooks,turns  Server-Sent Events 流；订阅一个或多个 topic（hooks event / agent turn events）
GET  /info                    便利端点，等价于 agent/info（panel boot 时少一次 round-trip）
```

`/rpc` 与 `/sse` 共享同一份 `rpc.Registry`；SSE 推送的 `data:` 行内容就是 JSON-RPC notification（`{"jsonrpc":"2.0","method":"hooks/event","params":{...}}`），与 stdio 通知格式完全一致——客户端代码两端复用。

**transport 鉴权**：

- bind 127.0.0.1（默认）→ 无 token
- bind 非 127.0.0.1 → **必须** `--rpc-token`，且仅接受 `Authorization: Bearer <token>`；缺失或错误返回 401
- token 以 hex(16 字节) 形式由 seek 启动时生成并印 stdout，或用户用 `--rpc-token=<custom>` 显式传

**CORS**：默认拒所有 cross-origin；`--rpc-allow-origin <pattern>` 显式开启给本地 web 面板使用（`http://localhost:*` glob）

### 3.4 Session 并发读

**问题**：session JSONL 正被主 seek 进程追加写时，RPC `session/get` / `session/messages` 同时读。当前 `internal/session.Load()` 是一次性全文件读，并发读写下行为未定义。

**方案**：

- 复用 `internal/session.Store` 已有的 sync.Mutex（每 Save 持锁）
- 新增 `session.Snapshot(sid) (*SessionSnapshot, error)`：持读锁 + 复制当前已落盘行 → 返回不可变快照
- RPC method 全部走 `Snapshot`，不直接 `Load`
- 主进程写入路径不变（依然持 Store.mu 写）；读端持读锁，写端持写锁，标准 RW 模型

**派生**：`internal/session.Store.mu` 改成 `sync.RWMutex`（当前是 sync.Mutex），所有 Save 改用 `Lock` / 所有 Load+Snapshot 改用 `RLock`。这是 §5 集成表里**对现有代码的唯一硬改动**。

### 3.5 错误模型

JSON-RPC 2.0 标准错误码：

```
-32700  parse error          客户端发的不是合法 JSON
-32600  invalid request      不符合 JSON-RPC 2.0 envelope
-32601  method not found     registry 里没注册
-32602  invalid params       params 校验失败
-32603  internal error       seek 内部错误
```

应用层错误用 `-32001..-32099` 自定义区间：

```
-32001  session not found
-32002  memory not found
-32003  project not found
-32004  permission denied    (read-only 期不会发生，预留)
-32005  rate limited         (M12.1 SSE 客户端过多时)
```

错误响应 `data` 字段可选携带结构化诊断（路径、suggestion 等），不被打印到面板默认视图，开发者模式可见。

## 4. Web 面板架构

### 4.1 站点结构（无框架，单 tarball）

```
seek-panel-v0.6.x/
├── index.html              # Shell + 路由（pure hashchange，无 history API）
├── style.css               # 单文件 CSS，<30 KB
├── app.js                  # 入口 + path router + RPC client，<50 KB minified
├── views/                  # 每个 view 是一个独立 JS 模块（ES modules，浏览器原生 import）
│   ├── sessions.js         # session list + 单个 session 浏览
│   ├── memory.js           # memory list + 单条详情（markdown 渲染走 marked.min.js）
│   ├── projects.js
│   ├── hooks.js            # hooks audit live stream（SSE）
│   ├── subagents.js        # M12.1
│   └── stats.js            # cache tracker + cost 趋势
├── vendor/
│   └── marked.min.js       # 唯一第三方 dep，~40 KB；纯渲染 markdown 用
└── README.md               # 启动说明：seek --rpc-listen 127.0.0.1:8080 然后浏览器打开 index.html
```

**总体积**：解压后 < 500 KB；用户从 GitHub Releases 下 `seek-panel-v0.6.x.tar.gz`，与 `seek` binary 并列发布。

**为什么不嵌主 binary**：保住 CLI 用户 "5 MB 单二进制" 体验。需要嵌入的用户走 build tag `panel`：`go build -tags panel ./cmd/seek` 编译出 `seek-with-panel`，启动多一个 `--panel` flag 直接 serve 静态站点。这条路 v0.7.0 之后再做。

### 4.2 SSE 推送：`/sse?topics=...`

订阅 topic：

| topic | 推送源 | notification method |
|---|---|---|
| `hooks` | `internal/hooks.Registry` 的 observer | `hooks/event` |
| `turns` | `pkg/agent` 的 event channel | `agent/event`（与 stdio transport 同 schema） |
| `subagents` | M12.1：`internal/subagent` spawn/state 变化 | `subagent/event` |

**实现**：每 SSE 客户端独立 goroutine，挂在 hooks Registry 的新增 observer slot 上。observer 触发时 fan-out 到所有订阅了对应 topic 的 SSE 连接。客户端断开 → goroutine 退出 → observer 自动注销。

**背压**：每客户端缓冲 channel 256 条；满则**丢老的**（前端 UI 显示 "stream lagged, refresh to resync" 提示），不阻塞 observer 主路径——这与 v3 §2.5 "失败降级而非阻断"一致。

### 4.3 启动方式

用户视角的最简流程：

```bash
# Terminal A：跑 seek + RPC
$ seek --rpc-listen 127.0.0.1:8080
rpc listening on http://127.0.0.1:8080

# Terminal B (或文件浏览器)：解压面板，浏览器打开
$ tar xzf seek-panel-v0.6.x.tar.gz
$ open seek-panel-v0.6.x/index.html
# 面板首页表单填 http://127.0.0.1:8080，点 connect
```

**配置持久化**：面板首次连接后把 URL + token 存在浏览器 localStorage；下次自动连。`?rpc=...&token=...` query 参数可一键带过来。

**多连接**：单 seek `--rpc-listen` 进程支持多个面板 tab 并发连接；每个 tab 独立 SSE 流。

## 5. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/rpc/server.go` | 抽 `Registry` + dispatch 改写；现有三 method 通过 wrapper 注册 | 中（机械重构） |
| `internal/rpc/`（新文件 `http.go` / `sse.go`） | HTTP+SSE transport adapter | 中 |
| `internal/rpc/methods/`（新包） | 每类 method 一个文件：`session.go` / `memory.go` / `hooks.go` / ... | 中-大 |
| `internal/session/store.go` | `sync.Mutex` → `sync.RWMutex`；新增 `Snapshot(sid)` | 小 |
| `internal/hooks` | 新增 SSE observer slot（与现有 observer 同 interface） | 极小 |
| `cmd/seek` | 新 flag：`--rpc-listen` / `--rpc-bind` / `--rpc-token` / `--rpc-allow-origin` | 小 |
| `cmd/seek-panel`（**不**做） | M12 阶段不引入独立 binary | 0 |
| `pkg/` | **不变** | 0 |
| 主 binary 大小 | 预期增量 < 100 KB（无前端资源 embed） | 极小 |
| Web 面板（`web/panel/`，**新目录**） | 静态站点源码；CI 单独打 tarball 发 release | 中 |

### 5.1 与 v5 柱 G（subagent）的集成

M12.1 等 v5 M11.0 ship 后启动——`internal/subagent` 暴露 `List(filter) []SubagentMetadata` + `Get(subSid)`，`internal/rpc/methods/subagent.go` 直接调用。**不**反向依赖 RPC——subagent 包是被 RPC 消费的纯数据源。

如果 v5 M11.0 因故延后，M12.0 仍可独立 ship——`subagent/*` method 暂时返回 `[]` + `panel_protocol_version = 0.6` 标识。

### 5.2 与 v5 柱 H（cron）的集成

`cron/list` 在 M11.2 ship 后由 M12.2 接入。设计上与 subagent 同模式。

### 5.3 与 v3 hooks 的集成

`hooks.Registry` 早就有 Observer 接口（v3 柱 B 已交付）；M12.0 在其上挂一个 `RpcObserver`，把事件 fan-out 到 SSE 客户端。observer 注册路径与现有 audit log observer 同栈，互不影响。

### 5.4 与 v3 checkpoint 的集成

`checkpoint/list` 走 `internal/checkpoint.List(sid)`（已有 API），M12.2 接入。

### 5.5 Prefix cache 影响

零。RPC 调用不进任何 agent prompt / messages 字节序列；SSE 推送的事件只读不写 transcript。

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | M12.0 后 `agent/info` 返回的 `methods` 数组包含全部新方法名，且数组序按字母序稳定 | 单元测试 |
| 2 | 抽出 `rpc.Registry` 后现有 stdio transport 的三个 method 行为完全不变 | 现有 `server_test.go` 零修改通过 |
| 3 | 同一 method 通过 stdio 和 HTTP+SSE 两条 transport 调用返回字节级相同的 result（除 JSON-RPC envelope 外） | 集成测试（两 transport 并行调同 method） |
| 4 | `session/messages` 分页：从头翻到尾遍历的 messages 顺序与 `session.Load(sid)` 完整结果一致 | 单元测试 |
| 5 | 主 seek 进程正在写 session JSONL 时 RPC `session/messages` 仍可读到已落盘部分，不读到半行 | 集成测试 + `-race` |
| 6 | `hooks/audit/tail follow=true` 客户端断开后对应 goroutine 在 < 100ms 内退出 | 单元测试（leaktest） |
| 7 | SSE 客户端缓冲满 256 条时丢老的，不阻塞 observer 主路径；面板显示 lagged 提示 | 单元测试 + 手测 |
| 8 | `--rpc-bind 0.0.0.0` 缺 `--rpc-token` 时启动失败并打印明确错误 | CLI 集成测试 |
| 9 | 非 localhost 请求缺 `Authorization: Bearer` 返回 401 + 标准 JSON-RPC `-32600` envelope | 集成测试 |
| 10 | Web 面板 tarball 解压后总大小 < 500 KB | CI artifact size check |
| 11 | 面板首次加载（冷 cache）→ session list 渲染完成 < 1s（localhost 链路） | 性能测试 |
| 12 | M12.1：spawn 一个子，面板 `/subagents` 视图通过 `subagents` SSE topic 实时收到 started → completed 事件 | 集成测试（需要 M11.0 ship） |
| 13 | 主 binary 体积增量（M12.0 ship 前后）< 200 KB | CI binary-size check |
| 14 | `go test -race ./internal/rpc/...` 全绿（HTTP+SSE 并发） | CI |
| 15 | 现有 v0–v4 测试套件零回归 | 现有测试 |

## 7. 实现计划

| 子 ms | 内容 | 估时 |
|---|---|---|
| **M12.0** | `rpc.Registry` 重构 + HTTP+SSE transport + `session.Snapshot` + 基础 method 集（agent/session/memory/project/hooks/stats）+ web 面板 MVP（sessions/memory/projects/hooks-tail 四视图） | ~5 天 |
| **M12.1** | subagent 三 method（list/get/tree）+ 面板 `/subagents` 视图 + `subagents` SSE topic | ~2 天（依赖 v5 M11.0） |
| **M12.2** | worktree / checkpoint / cron method + 面板对应视图 | ~1.5 天（依赖 v5 M11.1 + M11.2） |
| **合计** | | **~8.5 天** |

**发版策略**：

- **M12.0 → v0.6.x dot release**——**与 v5 完全并行可推进**，不依赖任何 v5 数据。优先 ship 让用户/作者立刻拿到调试入口与契约验证。
- **M12.1 → v0.7.0**（v5 ship 后第一个 dot release）——subagent 树是 panel 的 killer feature，与 v5 公布同步发声。
- **M12.2 → v0.7.0 或下个 dot**——按 v5 M11.2 完成度决定。

**实现顺序硬约束**：

1. 先做 `rpc.Registry` 重构（机械工作，单测可覆盖）
2. 再做 `session.Snapshot` + RW 锁化（牵动 internal/session 全局，先稳）
3. 然后扩 read-only method（每个独立 PR）
4. **最小 panel** 与 method 并行——一个 method ship 一个 panel view，闭环验证 method 设计
5. SSE transport 与 hooks/event observer 在基础 method 全部 ship 之后再上——避免 transport 复杂度与 method 设计同时变动

**并行可行性**：M12 与 v5 柱 G/H 完全独立，由不同人推也行。`internal/session.Store.mu` 改 RWMutex 是唯一与 v5 有潜在交集的改动——建议优先 land，让 v5 实现感知到锁语义已改。

## 8. 风险

| 风险 | 缓解 |
|---|---|
| Method 命名为"调试方便"凑而不为"应用消费"凑 → 应用阶段大面积 deprecation | M12.0 设计 review 时显式问 "如果未来移动应用要做 X 视图，这个 method 够不够？" 把 5 个想定 app 视图（session 列表、memory 浏览、subagent 树、cost dashboard、hooks log）作为 method 设计标准回路 |
| 把 `--rpc-listen` 不小心 bind 到 0.0.0.0 暴露给局域网 | fail-closed：bind 非 127.0.0.1 缺 token 直接拒启动；启动横幅红字打印实际 bind 地址 |
| SSE 客户端泄漏 goroutine | leaktest 覆盖；observer 注销路径 panic-safe（defer 兜底） |
| session 写入端 `Lock` 持有期间长时间阻塞 `RLock` 读端（如大文件 Save） | Save 路径已 append-only 单行 fsync；典型 Lock 持有 < 1ms。监测：metrics 加 `session_save_lock_ms` 直方图 |
| 主 binary 因引入 net/http 涨太多 | 当前 seek 已经 import 部分 net 包；预估 net/http 净增 ~70 KB（go build 测量过同类项目）；CI 加 binary-size diff 报警 > 200 KB 阻塞 PR |
| Web 面板渲染 markdown / 长 transcript 卡顿 | virtual scrolling + lazy markdown 渲染（只渲染 viewport 内 message）；超长 transcript 折叠按 100 message 分页 |
| 用户用 Chrome / Safari / Firefox 表现不一致 | 只用 fetch / SSE / ES modules 三个浏览器标准；不依赖 polyfill；README 写明最低版本（Chrome 90+ / Safari 15+ / Firefox 90+） |
| 用户在 Web 面板和 TUI 同时操作 → session 状态显示不一致 | SSE `agent/event` topic 转播 TUI 端的 turn 事件，面板可订阅做实时同步；不订阅时面板手动 refresh |
| Method 集与未来应用真实需求脱节，浪费两次 schema 设计 | M12.0 ship 后 dogfood 一周再开 M12.1；如发现关键 schema 漏洞，宁可在 M12.1 一起改也不要硬 ship |
| Web 资源 tarball 与 seek binary 版本不匹配（用户下旧 panel 连新 seek） | `agent/info` 返回 `panel_protocol_version` 与 `min_panel_version`；面板启动比对，不兼容时显示明确错误 + 升级 URL |
| HTTP server 启动失败（端口占用、权限不足）→ seek 进程意外退出 | startup 路径 fail-graceful：listener 失败 → 退回 stdio-only + stderr warning，TUI 仍可用 |

## 9. 后续版本

- **v0.7.0+**：嵌入式 panel binary（build tag `panel`，opt-in 编译产物，用户想要"单 binary 跑全部"的可走）
- **v0.7.0+**：写操作 method（`session/branch` / `memory/add` / `agent/cancel`）——为应用阶段铺路；面板加上"开发者模式"按钮才暴露
- **v0.7.0+**：HTTP transport 加 WebSocket upgrade 选项（双向 streaming，比 SSE 表达力强）；保留 SSE 作为简单订阅路径
- **v0.7.0+**：cross-project 视图（一次看所有 `~/.seek/projects/` 下的数据）
- **v0.8.0+（应用阶段开始）**：method 集冻结为 stable v1；deprecation 通道启用；面板分裂为"内置调试 panel"与"独立桌面 / 移动应用"两条路径，共享同一份 v1 contract
