# Feature: LSP find-references（v6 柱 L · 瘦身版）

**所属版本**：v6（单点工具补齐 umbrella）
**前置阅读**：[`v6.md`](v6.md) §3.4 柱 L + §2 跨柱约束、[`docs/comparison.md`](../comparison.md) §1（LSP 行）、[`feature-bash-monitor.md`](feature-bash-monitor.md)（柱 K——**会话级子进程生命周期**的直接先例）、`pkg/mcp/client.go`（JSON-RPC over stdio + init handshake 先例）
**状态**：🚀 已交付（v6 柱 L · 瘦身版）。`internal/lspclient`（手写 stdlib JSON-RPC + Content-Length 帧 + 读循环解复用）→ manager（懒启动 / 单 session 单 server / crash 重启 / 缺 binary 降级 / Shutdown / 会话级 ctx）→ `internal/tools/references`（1-based↔0-based + symbol 列定位 + 输出截断 + ReadOnly + grep 降级）→ `cmd/seek` 注入 + Shutdown + sysprompt。**零新依赖**（stdlib-only）；新增测试全过，`internal/{lspclient,tools/references,sysprompt}` 全 `-race` 绿，全仓 `go test ./...` 绿，Windows 交叉编译通过。
**目标里程碑**：M-L.1 ~ M-L.4（全部落地）
**目标发版**：v0.7.x
**估时**：~2-3 天（瘦身版；原全 5-op 版估 ~4-5d）

**范围决策（gut-check 后收窄）**：经 ROI 评估，LSP 五件套里**唯一 grep+read+build 替代不了的**是语义级 `references`（"谁调用了它"——grep 命中所有同名方法、漏别名导入、误中注释/字符串）。`definition`/`hover` 在 Go 里被 grep 覆盖大半、`diagnostics` 被 `go build`（柱 K 后台跑）吃掉。所以**只做 `references`**；其余 op 显式延后（基础设施留好扩展位，见 D1）。

**协议层**：`go.lsp.dev/jsonrpc2`（Content-Length 帧 + 请求-响应关联）+ **手写 ~5 个消息类型**（references-only 需要的子集很小，不必拉整个 `go.lsp.dev/protocol` 及其重传递依赖如 zap）。

---

## 1. 动机

seek 当前"谁调用了这个符号"只能 grep 字符串：

```
grep "\.Kill(" → 命中 bgjob.Kill、os/exec 的 Kill、注释里的 "Kill"、测试里的 mock……
```

——噪声大、漏别名导入（`import bg "…/bgjob"` 后的 `bg.Kill`）、误中字符串/注释。改一个导出 API 前想确认"谁在用"，grep 给不了可信答案。这是 LSP **唯一**对 coding agent 有硬增量的能力：`textDocument/references` 返回**语义解析后**的真实引用点。

其余 LSP 能力为何**不做**（gut-check 结论）：
- `definition`：grep `func Xxx` 通常 1-2 跳到；Go 里增量很边际。
- `hover`：read 源码 / 模型本就认识常见 API。
- `document/workspace_symbols`：grep + 结构基本够。
- `diagnostics`：`go build ./...` / `tsc --noEmit` 更权威，且柱 K 之后可后台跑。

把这些砍掉省下 ~2 天，且避免"为了凑 parity 铺一堆边际功能"。

---

## 2. 设计目标与不做什么

### 目标

1. **新 `references` 工具**：给 `file` + `line`（+ `symbol` 定位列），返回该符号的**语义引用列表**（≤50 条 `path:line:col` + snippet）。
2. **懒启动 + 会话级缓存**：首次调用按项目根/扩展名检测语言并启动 gopls / pyright / typescript-language-server；实例缓存在会话级 manager，跨 turn 复用（冷启动昂贵，绝不每次重启）。
3. **协议层用库扛难点**：`go.lsp.dev/jsonrpc2`（帧+关联）+ 手写最小消息类型（initialize / didOpen / references / shutdown）。**依赖限定在 `internal/lspclient` 内**，`pkg/deepseek` 零依赖不变。
4. **优雅降级**：binary 没装 → 清晰错误 + 安装命令；server crash → 下次调用自动重启 + re-init；无可用 server → 工具描述引导**回退 grep**。
5. **位置坐标系归一**：对外 1-based 行（grep/编辑器惯例），对内转 LSP 0-based line+character（头号 footgun，见 §8）。
6. **只读**：标 `ReadOnlyTool`，无 `permission.Policy`（启动固定可信 binary ≠ 任意 exec，类比 git 工具 spawn `git`）。

### 不做什么

- ❌ **definition / hover / document_symbols / workspace_symbols / diagnostics**——本柱不做。`internal/lspclient` 做成 operation-agnostic（能发任意 LSP method），将来要加某个 op = 加一个薄 tool wrapper，不重构（D1）。
- ❌ **LSP server 安装管理**——用户负责装 + 在 PATH。
- ❌ **写操作 / 实时 didChange**——seek 改文件后不主动通知；query 前 `didOpen` 当前字节让 server 重读（D6）。
- ❌ **拉整个 `go.lsp.dev/protocol`**——references-only 类型子集小，手写更轻（避免 zap 等重传递依赖）。
- ❌ **跨 session 共享 server**——单 session 单 server 是基线。
- ❌ **改 `pkg/agent` / `pkg/deepseek` / `internal/permission` 接口**（v6 §2.1）。

---

## 3. 跨柱约束（继承 + 柱 L 特有）

| 约束 | 柱 L 如何满足 |
|---|---|
| **prefix-cache 字节确定性** | tool schema 是 `[]byte` const；结果随轮变化、不进 schema。 |
| **输出 write-time cap** | `references` 可能上百条 → ≤50，尾部 `… N more（用更具体的位置收窄）…`。 |
| **零常驻 daemon** | language server 由 live session 持有——不写盘、不跨重启、`Shutdown` 随会话退出杀光。与柱 K 后台 bash **同性质**（会话内子进程，会话死则进程死），见下。 |
| **permission** | `references` 只读、不注入 Policy；启动固定 binary 非任意命令（类比 git/read/grep 无门）。 |
| **新增外部依赖（柱 L 独有）** | `go.lsp.dev/jsonrpc2` **仅在 `internal/lspclient` 内** import；`pkg/deepseek` 零依赖不变。CLAUDE.md「新依赖置于合理边界后」——边界就是这个包。M-L.1 先 `go mod why` 体检 jsonrpc2 的传递依赖。 |
| **v6 §2.1 不碰核心接口** | 新包 `internal/lspclient`（协议+manager）+ `internal/tools/references`（wrapper）。`pkg/agent` 零改动。 |
| **v6 §2.2 独立可回滚** | 不依赖柱 K（生命周期是平行模式而非代码依赖）；删 `references` 工具不影响任何其他柱。 |
| **失败降级** | binary 缺失 / server crash / 超时都返回结构化错误，引导回退 grep。 |

### 与柱 K 的生命周期类比（关键设计支点）

柱 K 的 D5 教训——**会话级子进程绝不能绑 turn ctx**——在柱 L **再次成立且更尖锐**：gopls 冷启动 5-15s + 缓存全模块索引，一次 turn 取消就重启会要命。

| | 柱 K 后台 bash | 柱 L language server |
|---|---|---|
| 生命周期 | 会话级（`bgjob.Manager`） | 会话级（`lspclient.Manager`） |
| 清理 | `Manager.Shutdown()` 在 `run()` 退出杀光 | `shutdown`→`exit`→kill，杀光 |
| 接线 | `cmd/seek/main.go` `run()` 内构造 + `defer Shutdown` | 同款 |

`internal/lspclient` 是与 `internal/bgjob` 并列的"会话级子进程管理"兄弟，互不 import（抽象不同：bgjob 是 fire-and-forget shell job，lspclient 是有状态 JSON-RPC peer）。

---

## 4. 设计决策

### D1 — 工具形态：单一 `references` 工具（不叫 `lsp`）

**选：工具名 `references`，专做一件事。** 不用 `lsp` + `operation` 枚举。

- 理由：references-only 时单值枚举很别扭；**功能名对模型选择更友好**（"我要找谁调用了 X" → `references` 直接命中；`lsp` 要模型先知道"LSP=references"）。符合 seek 功能命名（read/grep/edit/monitor）。工具名/描述是行为引导的最高杠杆（CLAUDE.md）。
- **不丢扩展性**：`internal/lspclient` 做成 operation-agnostic（`Call(method, params)`）。将来真要 `definition`，加一个薄 tool wrapper 即可，不重构 client/manager。

### D2 — 位置寻址：grep 找声明，LSP 找引用（分工）

**选：`references(file, line, symbol?)`，位置来自模型先前的 grep/read。**

- 典型流程：模型 `grep "func Kill"` → 拿到 `internal/bgjob/bgjob.go:230` → `references(file="internal/bgjob/bgjob.go", line=230, symbol="Kill")`。
- 这是**最干净的分工**：grep 找声明（它的强项，通常 1 跳）；LSP 找语义引用（只有它能干）。**不需要** `documentSymbol`/`workspace/symbol` 解析——省掉一整块。
- `symbol` 用于在该行定位精确列：工具在 `line` 上找 `symbol` 首次出现的列。给了显式 `character` 则直接用。`textDocument/references` 的 position 必须落在标识符上——`symbol` 定位保证这点。

### D3 — 语言检测 + binary 解析

**选：双路检测**——项目根 marker（`go.mod`→gopls；`pyproject.toml`/`setup.py`→`pyright-langserver --stdio`；`tsconfig.json`/`package.json`→`typescript-language-server --stdio`）+ 文件扩展名兜底。binary 不在 PATH → `[references: gopls not found in PATH; install: go install golang.org/x/tools/gopls@latest]`，不崩不重试。

> 保留 gopls/pyright/tsserver 三语言——`references` 的**最高价值恰在 TS/Python**（动态语言 grep 最弱）。砍语言会砍掉这功能的主要卖点。集成测试以 gopls 为主（CI 有 Go），其余 best-effort。

### D4 — server 生命周期（会话级，不绑 turn ctx）

**选：`lspclient.Manager` 持 `map[lang]*Server`，懒启动、单 session 单 server、`Shutdown` 杀光。**
- 启动不绑 turn ctx（柱 K D5 教训）：server 绑 manager 生命周期。
- 冷启动期间首次 `references`：用 turn ctx 做**调用超时/取消**（Esc 放弃这次查询）但**不杀 server**——server 继续 init，下次命中热缓存。观察者绑 turn、被观察者绑 session。
- crash 检测：jsonrpc2 `Conn.Done()` 关闭 / 进程 `Wait` 返回 → 标记死亡；下次调用重启 + re-init。

### D5 — 协议层：jsonrpc2 库 + 手写最小类型

**选：`go.lsp.dev/jsonrpc2` 扛 Content-Length 帧 + 请求-响应关联 + 通知 dispatch；手写 references-only 需要的 ~5 个消息类型。**

- 需要的 LSP 子集就这些：`initialize` / `initialized` / `textDocument/didOpen` / `textDocument/references` / `shutdown` / `exit`。类型小，手写 trivial。
- **不拉 `go.lsp.dev/protocol`**：它带 `go.uber.org/zap` 等重传递依赖，为 1 个 operation 不值。
- 服务端通知（`publishDiagnostics`/`logMessage`/`$/progress`）→ Handler 直接丢。
- M-L.1 第一步 `go mod why`/`graph` 体检 `jsonrpc2` 自身的传递依赖；若仍偏重，Content-Length 帧本身 ~40 行可手写兜底（但 jsonrpc2 是合理默认）。

### D6 — didOpen 同步

**选：query 前 `textDocument/didOpen`（带当前磁盘字节）。** seek 改文件后不发 didChange；每次 references 前读当前字节 didOpen（已 open 先 didClose），保证 server 看到当前内容。

### D7 — 权限 / ReadOnly

**选：`references` 标 `ReadOnlyTool`，不注入 `permission.Policy`。** 只读查询；启动固定可信 binary 非任意 exec。plan-analyze 放行、可并发批处理（调用有界超时，安全）。

---

## 5. 工具 schema + LSP 子集

**`references` schema 草案**：
```json
{
  "type": "object",
  "properties": {
    "file":      {"type": "string",  "description": "Path (relative to working dir) of a file where the symbol appears."},
    "line":      {"type": "integer", "minimum": 1, "description": "1-based line of the symbol (from a prior grep/read). Converted to LSP 0-based internally."},
    "symbol":    {"type": "string",  "description": "The symbol name on that line; used to find its exact column. Required unless character is given."},
    "character": {"type": "integer", "minimum": 1, "description": "Optional 1-based column of the symbol; used directly instead of locating symbol."}
  },
  "required": ["file", "line"],
  "additionalProperties": false
}
```

**LSP 交互**：lazy `initialize`→`initialized`（每 server 一次）→ 每次调用 `didOpen`(current bytes) → `textDocument/references`（`context.includeDeclaration=true`）→ 渲染。

**wire-format**：
- 成功：`<N> references to <symbol>:` + ≤50 × `path:line:col  | <snippet>`，截断时尾部 `… N more …`。
- 无引用：`[references: no references to <symbol> at <file>:<line>]`。
- binary 缺失：`[references: <server> not found in PATH; install: <cmd>]`。
- 超时：`[references: timed out after Ns; try grep as a fallback]`。

---

## 6. 测试（load-bearing — 对齐 CLAUDE.md "test the failure modes"）

**`internal/lspclient`（mock server，无需真 binary）**：mock = in-process peer，用同一个 `jsonrpc2` 说话、给 canned 响应。帧/关联是库的责任、库已测——我们测自己那层（handshake/通知 Handler/超时/取消/crash）。
| 测试 | 覆盖 |
|---|---|
| `TestClient_InitHandshake` | initialize→initialized 封装 |
| `TestClient_NotificationHandler` | publishDiagnostics/log/progress **丢弃**、不阻塞调用 |
| `TestClient_Timeout` | 调用超时返回、不挂死 |
| `TestClient_CtxCancel` | Esc 取消调用、**不杀 server**（D4） |
| `TestClient_ServerCrash` | server 中途退出 → 调用报错、标记死亡 |
| `TestManager_LazyStart_OnePerLang` | 首调用启动、二次复用 |
| `TestManager_RestartAfterCrash` | crash 后下次调用重启 + re-init |
| `TestManager_MissingBinary` | binary 缺失 → 清晰错误，不崩 |
| `TestManager_Shutdown_KillsAll` | 会话退出杀光（shutdown→exit→kill） |
| `TestManager_Concurrent` | `-race`：并发 query/启动/shutdown |

**`internal/tools/references`**：
| 测试 | 覆盖 |
|---|---|
| `TestRefs_PositionNormalization` | 1-based 行 → 0-based LSP（头号 footgun） |
| `TestRefs_SymbolColumnLocate` | 在 line 上定位 symbol 列；显式 character 走 directly |
| `TestRefs_OutputCap` | >50 引用 → 截断 + `… N more …` |
| `TestRefs_ReadOnlyMarker` | 实现 `ReadOnlyTool` 返回 true |
| `TestRefs_MissingFile` / `_SymbolNotOnLine` | 清晰错误 |

**集成测（CI-gated / skip-if-not-installed）**：真 gopls 对 seek 自身 repo 跑 `references` on 已知符号（如 `bgjob.Manager.Kill`），断言命中已知调用点。pyright/tsserver best-effort，pin 已测版本、文档注明。

---

## 7. 里程碑

| M | 内容 | 产出 |
|---|---|---|
| **M-L.1** | 先 `go mod why` 体检 jsonrpc2 传递依赖；`internal/lspclient`：spawn server 接 jsonrpc2 stdio + 通知 Handler（丢弃）+ init handshake + operation-agnostic `Call` + 手写 ~5 消息类型 | client + mock-server 单测 |
| **M-L.2** | `internal/lspclient/manager.go`：语言检测 + 懒启动 + 单 session 单 server + Shutdown + crash 重启 + 缺 binary 降级 | manager + 单测（`-race`） |
| **M-L.3** | `internal/tools/references`：position 归一 + symbol 列定位 + didOpen + references 调用 + 输出截断 + ReadOnly | 工具 + 单测 |
| **M-L.4** | `cmd/seek` 构造 manager + `defer Shutdown` + 注册工具 + `sysprompt`（找引用用 references；找定义/符号仍用 grep；不可用回退 grep）+ 文档（guide / 对比表 / README / v6 状态） | e2e + 文档同步 |

---

## 8. 风险与预埋 pitfall

| 风险 | 缓解 |
|---|---|
| **0-based vs 1-based 位置**（LSP 头号 footgun）：LSP 的 line **和** character 都 0-based；grep/编辑器/人是 1-based 行 | D2：工具边界统一 1-based 入、转 0-based 出；`TestRefs_PositionNormalization` 钉死 |
| **position 不落在标识符上** → references 返回空 | D2：用 `symbol` 在 line 上定位精确列，而非猜列 |
| **冷启动慢**（gopls 5-15s）：首次必慢 | 懒启动 + 调用超时 30s（init）；Esc 取消调用不杀 server，下次命中热缓存（D4） |
| **server 推送通知** | jsonrpc2 dispatch；Handler 直接丢、不阻塞调用 |
| **seek 改文件后 server 看旧内容** | D6：query 前 didOpen 当前字节 |
| **server crash / 版本 skew** | D4：crash 自动重启 re-init；CI pin 已测版本、文档注明 |
| **半坏仓里 LSP 降级/不准**（agent 常在修构建错时用）→ references 不可信 | 工具描述明示"仓不编译时结果可能不全，回退 grep"；超时/错误都引导 grep |
| **jsonrpc2 传递依赖偏重** | M-L.1 `go mod why` 体检；偏重则手写 ~40 行 Content-Length 帧兜底（D5） |
| **零 daemon 质疑** | 会话级、Shutdown 杀光，与柱 K 同性质 |

---

## 9. 落地后文档同步清单（全部完成 ✅）

- ✅ `docs/comparison.md`：§1 LSP 行 ❌→🔶**等效替代**；§小结"四件套"收敛；§核心结论标注五柱全交付。
- ✅ `README.md` / `README.zh.md`：Roadmap 柱 L 移入已交付（柱 I–M）+ "Next up → P2"；功能区加 `references` + guide 链接。
- ✅ `docs/guide-references.md`：新建"语义找引用"指南（安装前置 / grep→references 分工 / 生命周期 / 限制 / 降级）。
- ✅ `v6.md`：柱 L 行 ⬜→✅（瘦身版 references）；顶部 → 5/5 全交付。
- ⊘ `AGENTS.md` + `CLAUDE.md`：**评估后不加**（同柱 K）。`internal/lspclient` 无"只此包可变更 X"硬不变量；D2/D4 的依赖边界 + 生命周期约定已在包注释 + 本 PRD，再塞 CLAUDE.md 稀释架构红线。
