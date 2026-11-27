# Feature: Inspect RPC + Web 面板

**所属版本**：v0.6.x dot release（M12.0 并行于 v5；M12.1/M12.2 依赖 v5 数据）
**前置阅读**：[PRD v0](archive/v0.md) §4.5（JSON-RPC 服务模式）、[PRD v5 umbrella](archive/v5.md) §2.5（零常驻进程约束）、[`internal/rpc/server.go`](../../internal/rpc/server.go)（已有 method 集）
**状态**：🗄️ **已归档（2026-11-16，c_np5gzu）** · 数据面 **并入 [`feature-web-frontend.md`](feature-web-frontend.md)（2026-11-13）并已交付**——§3.1 method 集 / §3.5 错误码 / 命名稳定性纪律原样并入其 P3 资产面（W4.1–W4.6）；§3.3 HTTP+SSE transport 换宿主（`--rpc-listen` 于 TUI 进程 → `seek serve` web frontend 进程）；§4 独立 tarball 面板**取消**（go:embed 进单二进制）。**IDE 线（stdio transport + Registry 采纳）2026-11-16 关线归档**，理由见「交付实况」末段。数据面权威设计以 feature-web-frontend.md §3.3 为准。
**交付实况（2026-11-16 核对，W4.1–W4.6 已并 main；2026-11-20 增补 #6/#7）**：`POST /rpc` envelope + `internal/rpc/registry.go`（§3.2 形状，即两 transport 共享位；stdio `server.go` 保留自身 switch、尚未改挂）+ §3.5 错误码（W4.1，c_c4cs4a）；session / memory / project / stats / hooks 五族 method 在 `internal/rpc/methods/`（W4.2 c_zam44t · W4.3 c_ydglne · W4.6 c_igze7m）。**§6 #6/#7 收口（2026-11-20，W5 c_xz2bey）**：`seek serve` 的 `/events` 增 `hooks` topic（`internal/controlplane/webfrontend_hookstopic.go` + `internal/hooksconfig/follow.go`——全局审计 JSONL tail-follow，帧=审计行 verbatim，`?topic=hooks` 无需 conv 绑定）；`hooks/audit/tail` 的 `follow:true` 仍答 -32602 但指路 `/events?topic=hooks`。**fs/list 已落 web 面**（2026-11-22，W5 后增补卡 c_heffj3）：`internal/rpc/methods/fs.go` 经 registry 进 `seek serve` 的 `POST /rpc`（/bind 目录浏览器只读枚举口，可达根集收敛见 feature-web-frontend §13.1）——数据面 method 唯一权威仍在 registry，`--rpc` stdio 三 method 依旧冻结、永不扩。
仍未交付：`agent/info` 的 `methods` 清单与两 transport 字节一致（§6 #1/#3，随 IDE 线关闭不作）。口径被取代两项：§3.4 RWMutex 改造（web 侧跨进程磁盘读，append-only + 行级 parse 已够，§6 #5 相应改写）；「非 loopback 强制 token」（§6 #8）被更严的 web_addr scope 闸取代（2026-11-15 拍板，非 loopback/tailnet-CGNAT 一律拒启）。

**IDE 线关线决策（2026-11-16，c_np5gzu）**——前置问题「ACP（柱 P）已覆盖 IDE 集成主路径，Zed 外 IDE 还需要 `--rpc` stdio 吗？」答案**否**：① IDE 驱动面标准是 ACP（`seek acp`，[`feature-acp.md`](archive/feature-acp.md)，真 Zed GUI 验证）——ACP 自身即 stdio JSON-RPC，是 Zed/VS Code 驱动 agent 的标准协议（book ch.24 明言 `-rpc` 是自定协议、无工具链支持）；自定三 method 方言扩集买不到生态兼容。② 数据面唯一权威已落地：共享 `internal/rpc/registry.go` + `seek serve POST /rpc`（五族 method），HTTP 比 stdio 更可消费（curl/任意语言/浏览器），stdio 复刻同一 registry 没有消费者。③ `--rpc` stdio 自 M7 交付以来零已知增量消费者（仓库无 IDE 扩展、文档无用户证据；comparison.md R.2 IDE 集成行已按 ACP 计分）。处置：`--rpc` stdio 三 method（agent/prompt、agent/info、session/list）**冻结保留**——自有 switch、永不挂 registry、永不扩 method（§6 #2 已验证零回归）；`internal/rpc/registry.go` 是数据面 method 唯一权威（新 read-only method 只进 registry/serve）；IDE 驱动需求一律指 `seek acp`。§6 #1/#3 随线关闭；M12.1/M12.2 method 增量归 web-frontend P3/P2；#6/#7（hooks follow/SSE topic）宿主定为 web `/events` topic hub——**已做（2026-11-20，W5 c_xz2bey）**。
**目标里程碑**：M12.0 + M12.1 + M12.2
**目标发版**：M12.0 → v0.6.x dot；M12.1/M12.2 → v0.7.0

---

## 1. 动机

seek 长期路线图（用户口述）：**未来要做应用**——桌面 / 移动 / Web 前端作为 CLI 的补充消费层。当前 TUI 独占，内部状态（sessions / memory / hooks audit / subagent 树 / worktree / checkpoint 历史）只能靠文件系统 + `cat`+`jq` 触达，不具备喂应用的形态。

**应用阶段最大的风险不是 UI，是数据契约定不准**——v6/v7 应用启动后才发现"seek 没有 list-memory 的 stable method"，要么返工拉一年节奏，要么把契约硬塞进半成品 API 形成长期负债。

本 PRD 用**前置数据平面**化解风险：

- **复用已有 `--rpc` JSON-RPC 2.0** 服务面而非新建 HTTP API——已有 `internal/rpc/server.go`，应用是同一契约的另一类消费者
- **加 HTTP+SSE transport** 让浏览器/Tauri/Electron 消费同一 method 集（stdio 不变）
- **Read-only Web 面板**作为 transport + method 集的首个真实消费者，跑通端到端并暴露契约毛刺，兼作可视化调试入口
- **写操作延后到应用阶段**——MVP 只查不改，避开并发写控制

## 2. 设计目标与不做什么

### 目标

1. **方法名即契约**——ship 后破坏性 rename 需 minor bump + deprecation 通道，新增方法非破坏；`agent/info` 带 `methods` 清单 + 版本号供 capability negotiation。
2. **transport 与 method 解耦**——method 在 transport-agnostic registry 注册一次，stdio / HTTP 共享 dispatch。
3. **零常驻 daemon**（继承 v5 §2.5）——`--rpc-listen` 只在显式启动时存在，进程退出即停，与 TUI 同生命周期。
4. **Web 面板不进主 binary**——静态资源单发 tarball，解压后浏览器开 `index.html` 经 fetch+SSE 连本地 `seek --rpc-listen`，保住 "单二进制" 卖点。
5. **本地优先 + 默认安全**——默认 bind 127.0.0.1；非 localhost 需显式 `--rpc-bind 0.0.0.0` 且自动启用 token 鉴权。
6. **失败降级**——session 文件被主进程独占写时 RPC 仍能读（RW 锁或快照读）；hooks observer SSE 断开不影响 hooks 自身。

### 不做什么（v0.6.x dot / v0.7.0 明确延后）

- ❌ **写操作 method**——`session/edit` / `memory/add` / `agent/cancel` 等 mutating method 延后到应用阶段；本期 method 集**全部** read-only。
- ❌ **跨进程多 seek 状态同步**——面板只看连上的那个 `--rpc-listen` 进程。
- ❌ **认证 / 多用户**——本地工具单用户假设；token 仅是非 localhost bind 门槛。
- ❌ **Web 面板 SPA 框架（React / Vue / Svelte）**——纯 vanilla JS + HTML + CSS，解压后 < 500 KB，不引入 npm 工具链。
- ❌ **持久订阅 / 跨重启状态**——SSE 随 seek 退出即断，客户端负责重连，不做 server-side replay。
- ❌ **嵌入式 panel 二进制**（v0.6.x 不做，v0.7.0 评估）——"一个 binary 跑全部"走 `seek --rpc-listen --panel`（build tag `panel`，opt-in 编译）。

## 3. 数据模型与签名

### 3.1 Method 集（read-only 查询动词）

**M12.0 基础集**（与 v5 工作独立可 ship）：

| Method | params | result | 说明 |
|---|---|---|---|
| `agent/info` | — | `{version, model, yolo, plan, methods: [...]}` | 扩展现有：`methods` + `panel_protocol_version`。❌ 随 IDE 线关闭（2026-11-16）——stdio 冻结不扩；web /rpc 未挂，capability 清单若需要加在 web 面（未排期） |
| `session/list` | `{include_subagents?: bool}` | `[{sid, title, started_at, last_at, turns, model}]` | 扩展现有 `include_subagents` flag。✅W4.2（flag 宽容忽略，子代理走 W3.1 `GET /subagents` 与 M12.1） |
| `session/get` | `{sid}` | `{header, turns, cumulative_usage}` | header + 元数据，不含 messages。✅W4.2 |
| `session/messages` | `{sid, cursor?: string, limit?: int=50}` | `{messages: [...], next_cursor?: string}` | cursor 分页（line offset），稳定于追加。✅W4.2（limit 上限 500） |
| `memory/list` | `{project_id?: string}` | `[{name, kind, mtime, tagline}]` | project_id 缺省 = 当前项目。✅W4.3（kind/mtime 无盘面对应，映射 name/tagline/updated_at + pinned/stale） |
| `memory/get` | `{project_id?, name}` | `{name, kind, body, mtime, links: [...]}` | 完整 markdown body。✅W4.3（body→content；links 无对应不造） |
| `memory/projects` | — | `[{project_id, root, last_active_at}]` | 跨项目 memory 入口枚举。✅W4.3（newest-LastSeen 序） |
| `project/list` | — | `[{project_id, root, sessions: int, memories: int}]` | 已知项目枚举（`~/.seek/projects/`）。✅W4.3 |
| `project/get` | `{project_id}` | `{root, agents_md_path?, hooks_toml_path?, ...}` | 单项目元数据。✅W4.3 |
| `hooks/list` | `{project_id?}` | `[{name, event, command, source}]` | 已配置 hooks（解析 hooks.toml）。✅W4.6（project+user 合并按 dispatch 序；不跑 trust gate / bash -n，静态跳过经 audit 呈现；tool/timeout_ms） |
| `hooks/audit/tail` | `{from_ts?, follow?: bool, project_id?}` | `[{ts, event, tool, ...}]` *或*流式 | 非 follow 一次返回。✅W4.6 一次性口径（follow:true → -32602 明拒；limit 默认 100/上限 1000 tail；project_id 经 session header CWD 归属）。**follow 面已交付（2026-11-20，W5 c_xz2bey）：`GET /events?topic=hooks`，帧=审计行 verbatim；follow:true 改指路该 topic，§6 #6/#7 收口** |
| `stats/cache` | `{sid?}` | `cache.Tracker.Cumulative() 全字段` | sid 缺省 = 当前 session。✅W4.3（口径改盘侧：header cumulative Usage + hit_ratio；缺省 = 最新 session） |

**M12.1 子代理集**（依赖 v5 M11.0 ship）：

| Method | params | result |
|---|---|---|
| `subagent/list` | `{parent_sid?, status?: "active"\|"completed"\|"failed"\|"all"}` | `[{sub_sid, parent_sid, type, status, description, ...}]`（折叠后的 §3.4 事件源状态） |
| `subagent/get` | `{sub_sid}` | `{sub_sid, parent_sid, type, status, description, worktree_path?, tokens, ts}` |
| `subagent/tree` | `{root_sid}` | 父-子嵌套树（v5 限深度 1，保留树形 schema 给 v6 嵌套） |

**M12.2 编排状态集**（依赖 v5 M11.1 + M11.2 ship）：

| Method | params | result |
|---|---|---|
| `worktree/list` | `{project_id?}` | `[{sub_sid, path, branch, changes, status}]` |
| `checkpoint/list` | `{sid?}` | `[{turn, ts, label, ref}]` |
| `cron/list` | — | `[{name, schedule, next_run_at, last_run_at, last_status}]`（柱 H） |

### 3.2 Method registry 重构

当前 `internal/rpc/server.go` 用 `switch case` 派发，加 method 必碰主循环。M12.0 第一步**抽出 registry**：

```go
// Method is one RPC method handler. Implementations should be pure
// (read-only side effects at most — never mutate state via RPC
// in v0.6.x / v0.7.0).
type Method func(ctx context.Context, params json.RawMessage) (any, error)

// Registry holds the dispatch table. Construct once at server start;
// safe for concurrent reads after RegisterFromXXX completes.
type Registry struct {
    methods map[string]Method
}

func NewRegistry() *Registry
func (r *Registry) Register(name string, m Method)
func (r *Registry) Dispatch(ctx context.Context, name string, params json.RawMessage) (any, error)
```

`Server.handleCall` 改为 `s.registry.Dispatch(...)`；现有三 method (`agent/prompt` / `agent/info` / `session/list`) 经 wrapper 注册保持向后兼容。

**关线注记（2026-11-16）**：Registry 本体**已交付**（`internal/rpc/registry.go`，即上述形状；宿主 = `seek serve` POST /rpc，web-frontend W4.1）。IDE 线关闭后 stdio `server.go` **永不**改挂本 registry：三 method 冻结为遗留控制面（理由见状态头）。数据面新 method 只进 registry / serve 一处。

**契约稳定性约束**：

- Method 名进 `agent/info` 的 `methods` 列表后**不能改名**——破坏性 rename 走 deprecation：先双名 + 旧名打 flag，至少一个 minor 后下线
- result **新增**永远 OK，**删除/重命名**需 minor bump
- params **新增可选**永远 OK，**新增必填**需 minor bump

### 3.3 Transport：stdio（保留）+ HTTP+SSE（新增）

**stdio**（现有，**已冻结**）：不动，每行一个 JSON-RPC 对象，IDE 集成用户零回归。2026-11-16 关线后**永久冻结**——不挂 registry、不扩 method；IDE 集成主路径是 ACP（`seek acp`，[`feature-acp.md`](archive/feature-acp.md)），数据查询走 `seek serve` /rpc。

**HTTP+SSE**（M12.0 新增）：

```
seek --rpc-listen 127.0.0.1:0          # 自动选端口，stdout 印 `rpc listening on http://127.0.0.1:54321`
seek --rpc-listen 127.0.0.1:8080       # 指定端口
seek --rpc-bind 0.0.0.0 --rpc-token X  # 非 localhost 显式开启 + 必须 token（fail-closed）
```

HTTP 端点结构：

```
POST /rpc                     单次 method 调用，body = JSON-RPC 2.0 envelope
GET  /sse?topics=hooks,turns  SSE 流；订阅一个或多个 topic（hooks event / agent turn events）
GET  /info                    便利端点，等价 agent/info（panel boot 少一次 round-trip）
```

`/rpc` 与 `/sse` 共享同一份 `rpc.Registry`；SSE 的 `data:` 行就是 JSON-RPC notification（`{"jsonrpc":"2.0","method":"hooks/event","params":{...}}`），与 stdio 通知格式一致，客户端代码两端复用。

**transport 鉴权**：

- bind 127.0.0.1（默认）→ 无 token
- bind 非 127.0.0.1 → **必须** `--rpc-token`，仅接受 `Authorization: Bearer <token>`；缺失/错误返回 401
- token 以 hex(16 字节) 由 seek 启动时生成并印 stdout，或用 `--rpc-token=<custom>` 显式传

**CORS**：默认拒所有 cross-origin；`--rpc-allow-origin <pattern>` 显式开启给本地面板（`http://localhost:*` glob）

### 3.4 Session 并发读

**问题**：主 seek 进程追加写 session JSONL 时 RPC `session/get` / `session/messages` 同时读；`internal/session.Load()` 是一次性全文件读，并发读写行为未定义。

**方案**：

- 复用 `internal/session.Store` 已有的 sync.Mutex（每 Save 持锁）
- 新增 `session.Snapshot(sid) (*SessionSnapshot, error)`：持读锁 + 复制已落盘行 → 不可变快照
- RPC method 全走 `Snapshot`，不直接 `Load`
- 写入路径不变（持 Store.mu 写）；读端 `RLock`、写端 `Lock`，标准 RW 模型

**派生**：`internal/session.Store.mu` 改 `sync.RWMutex`（当前 sync.Mutex），Save 改 `Lock` / Load+Snapshot 改 `RLock`。这是 §5 集成表里**对现有代码的唯一硬改动**。

### 3.5 错误模型

JSON-RPC 2.0 标准错误码：

```
-32700  parse error          客户端发的不是合法 JSON
-32600  invalid request      不符合 JSON-RPC 2.0 envelope
-32601  method not found     registry 里没注册
-32602  invalid params       params 校验失败
-32603  internal error       seek 内部错误
```

应用层错误走 `-32001..-32099` 自定义区间：

```
-32001  session not found
-32002  memory not found
-32003  project not found
-32004  permission denied    (read-only 期不会发生，预留)
-32005  rate limited         (M12.1 SSE 客户端过多时)
```

错误响应 `data` 可选携带结构化诊断（路径、suggestion 等），不进面板默认视图，开发者模式可见。

## 4. Web 面板架构

### 4.1 站点结构（无框架，单 tarball）

```
seek-panel-v0.6.x/
├── index.html              # Shell + 路由（pure hashchange，无 history API）
├── style.css               # 单文件 CSS，<30 KB
├── app.js                  # 入口 + path router + RPC client，<50 KB minified
├── views/                  # 每 view 一个独立 JS 模块（ES modules，浏览器原生 import）
│   ├── sessions.js         # session list + 单 session 浏览
│   ├── memory.js           # memory list + 详情（markdown 走 marked.min.js）
│   ├── projects.js
│   ├── hooks.js            # hooks audit live stream（SSE）
│   ├── subagents.js        # M12.1
│   └── stats.js            # cache tracker + cost 趋势
├── vendor/
│   └── marked.min.js       # 唯一第三方 dep，~40 KB，纯渲染 markdown
└── README.md               # 启动说明：seek --rpc-listen 127.0.0.1:8080 后开 index.html
```

**总体积**：解压后 < 500 KB；从 GitHub Releases 下 `seek-panel-v0.6.x.tar.gz`，与 `seek` binary 并列发布。

**为什么不嵌主 binary**：保住 CLI "单二进制" 体验。需要嵌入的走 build tag `panel`：`go build -tags panel ./cmd/seek` 出 `seek-with-panel`，多一个 `--panel` flag 直接 serve 静态站点——v0.7.0 之后再做。

### 4.2 SSE 推送：`/sse?topics=...`

订阅 topic：

| topic | 推送源 | notification method |
|---|---|---|
| `hooks` | `internal/hooks.Registry` 的 observer | `hooks/event` |
| `turns` | `pkg/agent` 的 event channel | `agent/event`（与 stdio 同 schema） |
| `subagents` | M12.1：`internal/subagent` spawn/state 变化 | `subagent/event` |

**实现**：每 SSE 客户端独立 goroutine，挂 hooks Registry 的新增 observer slot；触发时 fan-out 到订阅该 topic 的连接。客户端断开 → goroutine 退出 → observer 自动注销。

**背压**：每客户端缓冲 256 条，满则**丢老的**（UI 显示 "stream lagged, refresh to resync"），不阻塞 observer 主路径——同 v3 §2.5 "失败降级而非阻断"。

**交付注记（2026-11-24，c_n72agp 核对；交付 2026-11-20 W5 c_xz2bey）**——本节是设计时形状，实际 ship 的 hooks 流按三点对：①路由是 `GET /events?topic=hooks` 而非 `/sse?topics=a,b`——`seek serve` 的单一常开 SSE 口，topic 是**每连接一个**的查询维（与 `?topic=subagents` 同 hub、同 `follows`/`pushFollow`），多 topic 一次订阅不在本版；不挂进主连接的事件族（`delta`/`tool`/`usage`/`approval`）的理由：审计流可选、可关、可整窗重拉，独立连接让「关面板=断流=最后一个订阅者释放 follow」无需额外协议；②帧=`event: hooks` + `data: <审计行 JSON>`，**无** `{"jsonrpc":…,"method":"hooks/event"}` 信封——审计行本身即 v3 §3.6 对外 wire 格式（第三方工具 tail 同一文件），与 `hooks/audit/tail` 返回行字节同形；③`?topic=hooks` **无需 `/bind`**（审计日志机器全局，subagents 的绑定是「选哪个索引文件」，此处无对应物）；坏 topic / 未绑定 conv 在 SSE 头之前答 400，不开半死 event-stream。**页面侧闸**沿用 `syncTopicStream`（`togglePane` → `syncHooksStream`，关闭即 `close()`），判据只剩「面板开」，否则全局审计面在未 `/bind` 会话永远空白。`hooks/audit/tail` 的 `follow:true` 仍答 -32602，文案指路本 topic——§6 #6/#7 由此收口。

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

**配置持久化**：首次连接后 URL + token 存浏览器 localStorage，下次自动连；`?rpc=...&token=...` query 参数可一键带入。

**多连接**：单 `--rpc-listen` 进程支持多 tab 并发，每个 tab 独立 SSE 流。

## 5. 与现有系统的集成

| 子系统 | 集成点 | 改动量 |
|---|---|---|
| `internal/rpc/server.go` | 抽 `Registry` + dispatch 改写；三 method 经 wrapper 注册 | 中（机械重构） |
| `internal/rpc/`（新 `http.go` / `sse.go`） | HTTP+SSE transport adapter | 中 |
| `internal/rpc/methods/`（新包） | 每类 method 一文件：`session.go` / `memory.go` / `hooks.go` / … | 中-大 |
| `internal/session/store.go` | `sync.Mutex` → `sync.RWMutex` + `Snapshot(sid)` | 小 |
| `internal/hooks` | 新增 SSE observer slot（同 interface） | 极小 |
| `cmd/seek` | 新 flag：`--rpc-listen` / `--rpc-bind` / `--rpc-token` / `--rpc-allow-origin` | 小 |
| `cmd/seek-panel`（**不**做） | M12 不引入独立 binary | 0 |
| `pkg/` | **不变** | 0 |
| 主 binary 大小 | 增量 < 100 KB（无前端资源 embed） | 极小 |
| Web 面板（`web/panel/`，**新目录**） | 静态站点源码，CI 单独打 tarball 发 release | 中 |

### 5.1 与 v5 柱 G（subagent）的集成

M12.1 等 v5 M11.0 ship 后启动——`internal/subagent` 暴露 `List(filter) []SubagentMetadata` + `Get(subSid)`，`internal/rpc/methods/subagent.go` 直接调用；**不**反向依赖 RPC。

v5 M11.0 延后时 M12.0 仍可独立 ship——`subagent/*` 暂时返回 `[]` + `panel_protocol_version = 0.6`。

### 5.2 与 v5 柱 H（cron）的集成

`cron/list` 在 M11.2 ship 后由 M12.2 接入。设计上与 subagent 同模式。

### 5.3 与 v3 hooks 的集成

`hooks.Registry` 早有 Observer 接口（v3 柱 B 已交付）；M12.0 挂一个 `RpcObserver` 把事件 fan-out 到 SSE 客户端，注册路径与现有 audit log observer 同栈。

**交付注记（2026-11-24，c_n72agp 核对）**——实际推送源**不是** `hooks.Registry` observer：`seek serve` 与执行 hook 的 seek 是两个进程，observer 只活在后者的内存；web 面只能 tail 落盘产物——`internal/hooksconfig/follow.go`（`AuditFollower`，锚点=订阅时刻的文件末尾）stat/read 全局审计 JSONL，`internal/controlplane/webfrontend_hookstopic.go` 把新增行 verbatim 推给 `?topic=hooks` 订阅者。「互不影响」仍成立：跟读不注册 observer、不执行 hook、不跑 trust gate。

### 5.4 与 v3 checkpoint 的集成

`checkpoint/list` 走 `internal/checkpoint.List(sid)`（已有 API），M12.2 接入。

### 5.5 Prefix cache 影响

零。RPC 调用不进任何 agent prompt / messages 字节序列；SSE 推送的事件只读不写 transcript。

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | M12.0 后 `agent/info` 返回 `methods` 数组含全部新方法名，序按字母序稳定 | 单元测试 |
| 2 | 抽出 `rpc.Registry` 后现有 stdio transport 三 method 行为完全不变 | `server_test.go` 零修改通过 |
| 3 | 同一 method 经 stdio 与 HTTP+SSE 返回字节级相同 result（除 JSON-RPC envelope 外） | 集成测试（两 transport 并行调同 method） |
| 4 | `session/messages` 分页从头翻到尾的顺序与 `session.Load(sid)` 一致 | 单元测试 |
| 5 | 主 seek 写 session JSONL 时 RPC `session/messages` 仍读到已落盘部分，不读半行 | 集成测试 + `-race` |
| 6 | `hooks/audit/tail follow=true` 断开后 goroutine 在 < 100ms 内退出 | 单元测试（leaktest） |
| 7 | SSE 缓冲满 256 条时丢老的、不阻塞 observer 主路径，面板显示 lagged 提示 | 单元测试 + 手测 |
| 8 | `--rpc-bind 0.0.0.0` 缺 `--rpc-token` 时启动失败并打印明确错误 | CLI 集成测试 |
| 9 | 非 localhost 请求缺 `Authorization: Bearer` 返回 401 + 标准 `-32600` envelope | 集成测试 |
| 10 | 面板 tarball 解压后总大小 < 500 KB | CI artifact size check |
| 11 | 面板首次加载（冷 cache）→ session list 渲染 < 1s（localhost） | 性能测试 |
| 12 | M12.1：spawn 子，面板 `/subagents` 经 `subagents` SSE topic 实时收 started → completed | 集成测试（需 M11.0 ship） |
| 13 | 主 binary 体积增量（M12.0 ship 前后）< 200 KB | CI binary-size check |
| 14 | `go test -race ./internal/rpc/...` 全绿（HTTP+SSE 并发） | CI |
| 15 | 现有 v0–v4 测试套件零回归 | 现有测试 |

**交付注记（2026-11-16，W4.6 搬运核对；2026-11-20 增补）**——本表按设计时口径保留，逐条实况（✅=已核/已交付、🔲=开放、「替代」=被更严或不同实现取代）：

| # | 实况 | 落点 |
|---|---|---|
| 1 | ❌ 随 IDE 线关闭（2026-11-16）——stdio 冻结不扩 method；capability 清单若需要加在 web /rpc 面（未排期） | c_np5gzu 关线决策（状态头） |
| 2 | ✅ stdio 三 method 零改动——`server.go` 保留自身 switch，`server_test.go` 零修改绿；此即**终态**（永不挂 registry） | 2026-11-16 全量 `-race` 复跑 |
| 3 | ❌ 随 IDE 线关闭（2026-11-16）——stdio 冻结为 3-method 遗留面，两 transport 各自为已 ship 契约（`session/list` 两形状并存即此结果），不再求一致 | c_np5gzu |
| 4 | ✅ `session/messages` 分页遍历=全文件顺序（cursor=消息行 offset，追加稳定） | W4.2 表驱动钉死 |
| 5 | 替代——web 侧跨进程磁盘读（append-only JSONL + 行级 parse，尾读不锁）；§3.4 RWMutex 是 TUI 进程内问题 | §3.3 caveat（feature-web-frontend）；`-race` 全量绿 |
| 6 | ✅ 收口（2026-11-20，W5 c_xz2bey）——follow 面 = serve `/events` SSE `hooks` topic（审计 JSONL tail-follow，帧=审计行 verbatim）；断连随最后订阅者释放、goroutine 退出（`TestWeb_TopicHooksSharedRefcountAndLeak`，W3.2 口径）；`follow:true` 仍 -32602 但指路该 topic。**2026-11-24 c_n72agp 实机复核**（drive/c_n72agp）：`scripts/web-hooksaudit-manual.sh` 全绿——curl 九项（verbatim 帧 / retry:3000 / 15s 心跳 / 未知 topic 双名 / follow 指路）+ 无头六景（快照/实时追加/过滤/暂停恢复/上翻/断线补拉），`go test -race ./internal/controlplane/` 69.9s 绿 | W5 c_xz2bey（webfrontend_hookstopic.go + hooksconfig/follow.go） |
| 7 | ✅ 收口（2026-11-20，W5 c_xz2bey）——hooks topic 入同一 hub（`pushFollow` 满丢老不阻塞，`TestWeb_PushTopicDropsOldest` 语义共用）；面板「N 新行」即 lagged 提示，点计数或滚回贴底补渲染。**同日复核**：web-gate 过（built 31 698 692B vs baseline 31 557 490B，delta +141 202B / 预算 204 800B）；`scripts/web-page-review.sh` 32/32 镜头，审计面亮/暗 × 1440/390 四张实拍含五行审计（OCR 可读，见 c_n72agp 写回） | W3.2 + W5 c_xz2bey |
| 8 | 替代（更严）——web_addr scope 闸：非 loopback/tailnet-CGNAT 拒启 exit-2 | `TestWeb_AddrScope` / `TestWeb_RunForbiddenAddr` / `TestServePlane_BadWebAddrFails` |
| 9 | ✅（更严）——web 面全状态路由一律 token（401 家族），不分是否 loopback | W1.2 鉴权族测试 |
| 10 | 取消替代——tarball 被 go:embed 取代；体积由 CI web-gate 守（净增 <200KB） | W1.5（c_ph2zax） |
| 11 | 🔲 UI 线验收项，本卡不涉 | W4.4/W4.5 手测 |
| 12 | ✅ 收口（2026-11-20，c_4doebx）——`subagent/list`/`subagent/get` 挂 /rpc（同一磁盘折叠，`CodeSubagentNotFound=-32006`）；`tree` 不挂：v5 深度=1，树即带 parent_sid 的平表；`GET /subagents` 为兼容口，与 /rpc 同源同形（`TestWeb_SubagentMethodsMatchCompatEndpoint` 钉死）；面板数据源已切 /rpc，徽标三态 + 定时/事件双路刷新 | W3.1（c_7yofgo）/ W3.2（c_3736yl）/ c_4doebx |
| 13 | ✅ CI binary 体积 gate | W1.5 web-gate job |
| 14 | ✅ `go test -race ./internal/rpc/...`（含 methods 子包）绿 | 2026-11-16 复跑 |
| 15 | ✅ 全量 `-race` 套件零回归 | 2026-11-16 复跑（W4.6 卡内） |

## 7. 实现计划

| 子 ms | 内容 | 估时 |
|---|---|---|
| **M12.0** | `rpc.Registry` 重构 + HTTP+SSE transport + `session.Snapshot` + 基础 method 集（agent/session/memory/project/hooks/stats）+ 面板 MVP（sessions/memory/projects/hooks-tail 四视图） | ~5 天 |
| **M12.1** | subagent 三 method（list/get/tree）+ 面板 `/subagents` 视图 + `subagents` SSE topic | ~2 天（依赖 v5 M11.0） |
| **M12.2** | worktree / checkpoint / cron method + 面板对应视图 | ~1.5 天（依赖 v5 M11.1 + M11.2） |
| **合计** | | **~8.5 天** |

**发版策略**：

- **M12.0 → v0.6.x dot release**——**与 v5 完全并行**，不依赖任何 v5 数据，优先 ship 拿到调试入口与契约验证。
- **M12.1 → v0.7.0**（v5 ship 后第一个 dot）——subagent 树是 panel 的 killer feature。
- **M12.2 → v0.7.0 或下个 dot**——按 v5 M11.2 完成度决定。

**实现顺序硬约束**：

1. 先做 `rpc.Registry` 重构（机械，单测可覆盖）
2. 再做 `session.Snapshot` + RW 锁化（牵动 internal/session 全局，先稳）
3. 然后扩 read-only method（每个独立 PR）
4. **最小 panel** 与 method 并行——一个 method ship 一个 view，闭环验证设计
5. SSE transport 与 hooks/event observer 在基础 method 全 ship 之后再上，避免两处复杂度同时变动

**并行可行性**：M12 与 v5 柱 G/H 完全独立，可由不同人推。`internal/session.Store.mu` 改 RWMutex 是唯一潜在交集，建议优先 land。

## 8. 风险

| 风险 | 缓解 |
|---|---|
| Method 为"调试方便"凑而非"应用消费"凑 → 应用阶段大面积 deprecation | M12.0 设计 review 显式问 "未来移动应用做 X 视图，这 method 够不够？" 以 5 个想定 app 视图（session 列表、memory 浏览、subagent 树、cost dashboard、hooks log）为设计标准 |
| `--rpc-listen` 误 bind 0.0.0.0 暴露给局域网 | fail-closed：bind 非 127.0.0.1 缺 token 直接拒启动；启动横幅红字打印实际 bind 地址 |
| SSE 客户端泄漏 goroutine | leaktest 覆盖；observer 注销路径 panic-safe（defer 兜底） |
| `Lock` 长持有阻塞 `RLock` 读端（大文件 Save） | Save 已 append-only 单行 fsync，典型持有 < 1ms；监测加 `session_save_lock_ms` 直方图 |
| 引入 net/http 撑大 binary | 已 import 部分 net 包；预估净增 ~70 KB（同类项目实测）；CI binary-size diff > 200 KB 阻塞 PR |
| 面板渲染 markdown / 长 transcript 卡顿 | virtual scrolling + lazy markdown（只渲染 viewport 内 message）；超长 transcript 按 100 message 分页折叠 |
| Chrome / Safari / Firefox 不一致 | 只用 fetch / SSE / ES modules 三标准，不依赖 polyfill；README 写最低版本（Chrome 90+ / Safari 15+ / Firefox 90+） |
| 面板与 TUI 同时操作 → session 状态不一致 | SSE `agent/event` topic 转播 TUI turn 事件；不订阅则手动 refresh |
| Method 集与真实需求脱节，浪费两次 schema 设计 | M12.0 ship 后 dogfood 一周再开 M12.1；关键 schema 漏洞宁可一起改也不硬 ship |
| tarball 与 seek binary 版本不匹配（旧 panel 连新 seek） | `agent/info` 返回 `panel_protocol_version` 与 `min_panel_version`；面板启动比对，不兼容时明确报错 + 升级 URL |
| HTTP server 启动失败（端口占用、权限不足）→ seek 退出 | fail-graceful：listener 失败 → 退回 stdio-only + stderr warning，TUI 仍可用 |

## 9. 后续版本

- **v0.7.0+**：嵌入式 panel binary（build tag `panel`，opt-in）
- **v0.7.0+**：写操作 method（`session/branch` / `memory/add` / `agent/cancel`）——为应用阶段铺路，面板"开发者模式"才暴露
- **v0.7.0+**：HTTP transport 加 WebSocket upgrade（双向 streaming 强于 SSE），SSE 保留为简单订阅路径
- **v0.7.0+**：cross-project 视图（一次看所有 `~/.seek/projects/`）
- **v0.8.0+（应用阶段开始）**：method 集冻结为 stable v1，deprecation 通道启用；面板分裂为"内置调试 panel"与"独立桌面 / 移动应用"，共享同一份 v1 contract
