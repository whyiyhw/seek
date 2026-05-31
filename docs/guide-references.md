# seek 语义找引用 — `references` 工具指南 / Semantic find-references guide

`references` 问语言服务器（gopls / pyright / typescript-language-server）**"谁调用/使用了这个符号"**——解析真实符号、跟踪别名导入、跳过注释和字符串。这是 grep 替代不了的那一件事。

> `references` asks the language server who calls/uses a symbol — resolving the real symbol, following aliased imports, ignoring comments and strings. It's the one thing a name-grep can't do.

> **范围说明**：seek 的 LSP 集成是**瘦身版**——只做 `references`。`definition`/`hover`/符号列表经 ROI 评估**故意不做**（Go 里被 grep + `go build` 覆盖，增量低；动态语言里 `references` 才是高价值的硬赢）。设计与取舍见 [`docs/prd/feature-lsp.md`](./prd/feature-lsp.md)。

---

## 1. 前置：装语言服务器 / Install the server

`references` 不管安装——你负责把对应 server 放进 PATH：

| 语言 | server | 安装 |
|---|---|---|
| Go | `gopls` | `go install golang.org/x/tools/gopls@latest` |
| Python | `pyright-langserver` | `npm i -g pyright`（或 `pip install pyright`） |
| TS/JS | `typescript-language-server` | `npm i -g typescript-language-server typescript` |

没装 / 不在 PATH 时，`references` 返回 `[references: gopls not found in PATH; install: … . Fall back to grep for now.]`，不崩。

---

## 2. 用法：grep 找声明，references 找引用 / Workflow

`references` 是**位置驱动**的——它需要符号出现的 `file` + 1-based `line`。典型分工是**先 grep 找声明、再 references 找所有引用**：

```
# 1. grep 找到声明在哪
grep "func Kill" internal/bgjob/bgjob.go   → bgjob.go:230: func (m *Manager) Kill(...)

# 2. references 拿到所有语义引用
references(file="internal/bgjob/bgjob.go", line=230, symbol="Kill")
→ 4 reference(s) to Kill:
  internal/tools/monitor/monitor.go:112:9   | if err := t.mgr.Kill(a.Job); err != nil {
  internal/bgjob/bgjob.go:240:18           | func (m *Manager) Shutdown() { … Kill(id) }
  …
```

- `symbol` 用来在那一行定位精确列；也可以直接给 1-based `character` 代替。
- 行号是 **1-based**（和 grep / 编辑器一致）——工具内部转成 LSP 的 0-based，你不用操心。
- 结果上限 **50 条**，超出标 `… N more …`；用更具体的位置收窄。

**什么时候用 grep 而不是 references**：找"X 定义在哪"、列符号、模糊搜索——grep 就够。`references` 专门回答**"谁用了它"**（改导出 API 前的安全网）。

---

## 3. 生命周期 / Lifecycle

- **懒启动**：首次对某语言调用 `references` 时才拉起 server（按 `go.mod` / `pyproject.toml` / `tsconfig.json` 等检测）。
- **会话级缓存**：server 实例跨 turn 复用（gopls 冷启动 5-15s 索引整个 module，绝不每次重启）。
- **冷启动期间按 Esc**：只取消**这次查询**，server 继续初始化、留在缓存，下次查询命中热缓存——不会白白重启。
- **会话退出全杀**：`shutdown` → `exit` → kill，绝不留 orphan。**零持久化、不跨重启**（与 [`seek cron`](./guide-cron.md) 的跨进程调度是两回事）。

---

## 4. 限制与降级 / Limits & fallback

| 情况 | 行为 |
|---|---|
| server 没装 | `[references: <server> not found; install: …]` → 回退 grep |
| server 超时（仍在索引） | `[references: … timed out …]` → 稍后重试或回退 grep |
| 文件扩展名无对应 server（如 `.rs`） | `[references: no LSP server configured for .rs …]` → 用 grep |
| **仓库不编译**（你正在修构建错时） | LSP 结果可能不全/不准——**这时直接信 grep** |
| 并发引用 > 50 | 截断 + `… N more …` |
| Windows | 可用，但进程清理走 `taskkill`，不如 Unix 进程组精确（降级路径） |

`references` 的设计前提是：**它是 grep 的精度升级，不是替代**。任何不确定/不可用的场景，回退 grep 永远是对的。
