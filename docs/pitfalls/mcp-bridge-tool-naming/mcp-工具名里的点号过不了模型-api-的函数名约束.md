### MCP 工具名里的点号过不了模型 API 的函数名约束——`mcp.<server>.<tool>` 在首个请求就 400

- **Saw**: ACP `mcpServers` 透传（卡 c_oyt6ft）设计稿命名 `mcp.<server>.<tool>`；实现后工具可注册、Bridge 可往返，但带 `tools` 的请求整轮被拒（`tool function names must match ^[a-zA-Z0-9_-]{1,64}$`）；MCP 允许点号（`card.read` 合法）、注册表不校验——只有模型 API 拒。
- **Why**: 合法字母表分层：MCP draft 允许 `[A-Za-z0-9_.-]`，模型 API（DeepSeek / OpenAI / Anthropic 一致）只允许 `^[a-zA-Z0-9_-]{1,64}$`；`tools.Registry` 不校验名字（还服务内部工具）；host 与 provider 间没人负责字母表。
- **Fix**: 三段式改双下划线 `mcp__<server>__<tool>`（dsh 同形），server/工具段折到安全字母表并截断 64 字符。折叠仅当 `NamePrefix` 非空（调用方主动命名）时；`mcp.json` 默认 `<server>__<tool>` 逐字节不变——存量 roster 是已 ship 契约。
- **Lesson**: 外部名字进模型 tools 数组，必须在进注册表时折成 `^[a-zA-Z0-9_-]{1,64}$`；等 provider 报错最贵（整轮 400）。
- **Refs**: `internal/acp/mcp.go`（`ToolPrefix`/`sanitizeSegment`）、`internal/tools/mcptool/bridge.go`（`NamePrefix`/`safeToolName`）、`docs/guide-zed.md` §5.5；同族：`docs/pitfalls/mcp-bridge-tool-naming/同一份-mcp-json-两次启动可能注册出不同的工具名.md`
