### MCP 工具名里的点号过不了模型 API 的函数名约束——`mcp.<server>.<tool>` 在首个请求就 400

- **Saw**: ACP `mcpServers` 透传（卡 c_oyt6ft）的设计稿把工具命名写成 `mcp.<server>.<tool>`；按字面实现后，工具确实注册成功、Bridge 也能真实往返调用，但请求一旦带 `tools` 数组就会被 provider 拒（`tool function names must match ^[a-zA-Z0-9_-]{1,64}$`）。MCP 自己允许点号（工具名 `card.read` 合法），宿主注册表也不校验——三层里只有最外层会拒，且拒的是**整轮**。
- **Why**: 工具名的合法字母表是**分层不同**的：MCP draft 允许 `[A-Za-z0-9_.-]`，模型 API（DeepSeek / OpenAI / Anthropic 一致）只允许 `^[a-zA-Z0-9_-]{1,64}$`。注册表（`tools.Registry`）不做名字校验，因为它同时服务内部工具；host 与 provider 之间没有任何一层负责"最后那道字母表"。
- **Fix**: 三段式改用双下划线：`mcp__<server>__<tool>`（dsh 同形），并且对 server 段与工具段都折叠到安全字母表 + 截断到 64 字符（`mcptool.safeToolName` + `acp.sanitizeSegment`）。折叠**只发生在调用方主动命名时**（`mcptool.ServerConfig.NamePrefix` 非空），`mcp.json` 的默认 `<server>__<tool>` 路径逐字节不变——存量 roster 是已 ship 契约，不能为了新车道改老行为。
- **Lesson**: 凡是把外部名字搬进模型 tools 数组的通道，命名必须在**进入注册表那一刻**就折成 `^[a-zA-Z0-9_-]{1,64}$`；等待 provider 报错是最贵的发现方式（整轮 400，且错误出现在离命名点很远的层）。设计稿里的点号命名不要照抄，先查一遍目标 API 的 pattern。
- **Refs**: `internal/acp/mcp.go`（`ToolPrefix`/`sanitizeSegment`）、`internal/tools/mcptool/bridge.go`（`NamePrefix`/`safeToolName`）、`docs/guide-zed.md` §5.5；同族：`docs/pitfalls/mcp-bridge-tool-naming/同一份-mcp-json-两次启动可能注册出不同的工具名.md`
