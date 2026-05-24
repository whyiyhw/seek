# MCP 生态集成：以 Semble 为第一验证目标

**主题**：seek 的 MCP 客户端（M5.4）已交付，但"能连 MCP server"不等于"agent 会用"。本 PRD 以 Semble 语义代码搜索为第一验证目标，定义 MCP 工具在 agent 循环中的最佳实践：prompt 工程、工具路由、效果评估。

**状态**：📐 设计稿。MCP 客户端基础设施已于 M5.4 交付（`45c71af`），本文是后续深度集成设计。

## 一、现状审计

### 1.1 已交付的 MCP 基础设施

| 模块 | 文件 | 功能 | 状态 |
|------|------|------|------|
| `pkg/mcp/` | client.go, types.go | JSON-RPC 2.0 over stdio，含 initialize / list_tools / call_tool | ✅ `45c71af` |
| `internal/mcpconfig/` | config.go | 从 `~/.seek/mcp.json` 加载配置，兼容 Claude Code 格式 | ✅ `45c71af` |
| `internal/tools/mcptool/` | bridge.go | MCP Tool → seek Tool 适配，含前缀去重（`server__tool`） | ✅ `398448e` |
| `cmd/seek/main.go` | L373–400 | 启动时调用 LoadServers 注册 MCP 工具到 Registry | ✅ 已集成 |
| `docs/book/chapter-12.md` | — | 完整的设计文档 | ✅ `b55aede` |

用户只需在 `~/.seek/mcp.json` 中添加配置，重启即可使用 MCP 工具。

### 1.2 但"能用"不等于"会用"

MCP 桥接只是给了 agent 调用外部工具的**能力**，但 agent 不会自动知道：
- 什么时候应该用 `mcp_semble_search` 而不是 `grep`
- 什么查询适合语义搜索，什么适合精确匹配
- 搜索结果返回后如何跟进 read / edit

当前 agent 的 system prompt 只提到内置工具（read / grep / edit / bash 等），对 MCP 工具一无所知。

## 二、Semble 深度集成方案

### 2.1 方案 A：Prompt 层引导（轻量）

不改 agent 代码，只更新 system prompt 和 AGENTS.md，告知 agent MCP 工具的存在和用途。

**改动点**：
- `pkg/agent/prompt.go` 的 `SystemPrompt()` 或 `workflowReminder` 追加 MCP 工具说明
- `AGENTS.md` 或 `CLAUDE.md` 中添加 Semble 搜索工作流

**优点**：零代码改动，风险低
**缺点**：agent 可能选择忽略；其他 MCP server 的工具也需要各自说明

### 2.2 方案 B：工具路由优化（中等）

在 `pkg/agent` 的工具调度层，对查询类型做分类，自动选择最合适的搜索工具：

- 符号/标识符查询（如 `authenticate`、`SaveToDB`）→ `grep`（精确匹配更快）
- 自然语言查询（如 "how is auth handled"、"find the db connection code"）→ `mcp_semble_search`
- 混合查询 → 两者并行 + 结果融合

**改动点**：
- `pkg/agent/agent.go` 新增 `toolRouter` 逻辑
- 需要识别查询类型的启发式规则（Semble 的 `is_symbol_query()` 可参考）

**优点**：自动路由，无需 agent 判断
**缺点**：侵入 agent 核心循环；查询分类不完美时可能误路由

### 2.3 方案 C：搜索→编辑闭环（深度）

构建"搜索→定位→读取→编辑"的完整 pipeline：
1. agent 收到用户需求
2. 自动调用 Semble 搜索相关代码
3. 从结果中提取文件路径和行号
4. 自动 `read` 命中代码的完整上下文
5. 执行 `edit` 修改

**改动点**：
- 新增一个 orchestration tool（如 `plan_and_edit`）封装上述流程
- 或由 agent 在单轮中自行编排（取决于模型能力）

**优点**：端到端自动化
**缺点**：复杂度高；可能过度抽象，降低灵活性

## 三、推荐路径：迭代式

### Phase 0：验证 Semble MCP 可用 ✅（当前状态）

```
~/.seek/mcp.json:
{
  "mcpServers": {
    "semble": {
      "command": "uvx",
      "args": ["--from", "semble[mcp]", "semble"]
    }
  }
}
```

手动验证 agent 能否正确调用 `mcp_semble_search`。

### Phase 1：Prompt 引导（轻量，1–2 天）

- 更新 `pkg/agent/prompt.go`，在 MCP 工具存在时追加使用说明
- 更新 `AGENTS.md` 加入 Semble 搜索工作流示例
- 效果衡量：对比启用前后 agent 使用 `mcp_semble_*` 的频率

### Phase 2：效果评估（数据驱动）

- 统计 `mcp_semble_search` 的调用次数和后续 read 行为
- 对比使用 Semble vs 纯 grep 找到目标代码的 token 消耗
- 决定是否需要更进一步集成（Phase 3）

### Phase 3：工具路由优化（可选，3–5 天）

如果 Phase 2 数据证明价值，在 agent 循环中增加查询类型检测和自动路由。

## 四、Semble 内部实现要点（供参考）

分析 Semble 源码后，其核心设计可供 seek 未来参考：

### 4.1 Tree-sitter AST 分块

```
source → tree-sitter parse → _merge_node_inner() 递归遍历 AST
  → 按函数/类边界分组 → _merge_adjacent_chunks() 合并小 chunk 到 ~1500 chars
  → 回退策略：不支持的语言用 chunk_lines() 行级分块
```

**对 seek 的启示**：如果要在 Go 中做代码分块，可以用 `github.com/smacker/go-tree-sitter`。

### 4.2 Hybrid 检索算法

```
查询 → 同时跑 BM25 + Dense Embedding (model2vec)
  → 分别转 RRF (Reciprocal Rank Fusion) 分
  → alpha * semantic + (1-alpha) * bm25 融合
  → boost: 符号定义 ✕3、文件名匹配、多 chunk 文件
  → penalise: 测试文件、生成文件降权
```

**对 seek 的启示**：BM25 部分可以纯 Go 实现（`github.com/geange/go-bm25`）作为 `grep` 的增强替代，不需要 embedding 也能获取得分 0.673 NDCG@10（BM25 自身），比 ripgrep 的 0.126 高很多。

### 4.3 Query 类型检测

```python
_SYMBOL_QUERY_RE = re.compile(
    r"^(?:"
    r"[A-Za-z_][A-Za-z0-9_]*(?:(?:::|\\|->|\.)[A-Za-z_][A-Za-z0-9_]*)+"
    r"|_[A-Za-z0-9_]*"
    r"|[A-Za-z][A-Za-z0-9]*[A-Z_][A-Za-z0-9_]*"
    r"|[A-Z][A-Za-z0-9]*"
    r")$"
)
```

符号查询 → `alpha=0.3`（更偏 BM25 精确匹配）
NL 查询 → `alpha=0.5`（均衡语义 + 精确）

**对 seek 的启示**：grep 工具可以检测查询类型，对符号查询走字面量匹配，对 NL 查询提示使用 MCP 搜索。

## 五、不做的（明确排除）

1. **不把 Semble 的 Python 代码迁入 seek** —— Model2Vec / vicinity / tree-sitter 的 Python 依赖链不符合 seek 的单二进制哲学
2. **不使用 Semble 作为库嵌入** —— 即使有 Go embedding 模型，16M 静态模型的效果上限有限，性价比不如 MCP 桥接
3. **不实现 MCP server** —— seek 只做客户端，不做为工具被调用的 server 端

## 六、关联文档

- `docs/book/chapter-12.md` —— MCP client 实现设计文档（已交付）
- `pkg/mcp/` —— MCP 协议客户端源码
- `internal/tools/mcptool/` —— MCP 工具桥接源码
- [MinishLab/semble](https://github.com/MinishLab/semble) —— 语义代码搜索项目
