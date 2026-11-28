### `-tools nosuch` 先 fork 全部 MCP 子进程再报 exit 2——参数校验排在副作用之后
- **Saw**: 2026-11-24 butler MCP 机器上，`seek --tools nosuch` 日志为「fork → `mcp: loaded 9 tool(s)` → 才发现名字不存在」；`--tools read` 同样全量 fork（只省 244 tok）。
- **Why**: 唯一校验 `applyToolsWhitelist` 排在最后一个 `reg.Add` 后（MCP 工具名要等 `ListTools`），`mcpconfig.Load()`+`LoadServers()` 在前——校验晚于副作用。
- **Fix**: `checkToolsWhitelistPreMCP` 移到 `LoadServers` 前，同一份 config 三分：内建名（已注册或 `postMCPTBuiltInTools`）放行；能证明无 server 提供的当场 exit 2；命中已配置 `<server>__` 前缀或 `"prefix": false` 的可注册裸名推迟；措辞同 `errToolsUsage`→exit 2。
- **Lesson**: 校验门要放在所保护副作用之前，不是数据齐后的方便处；前置门只能拒「可证明不可能」，缺口须命名并用测试钉死（`TestCheckToolsWhitelistPreMCP_Table` 三分表 + E2E 按子进程 pid 文件计 fork），否则会被收紧成「未知即拒」。
- **Refs**: `cmd/seek/main.go`（`run()`）、`cmd/seek/tools_precheck.go`、`cmd/seek/tools_precheck_test.go`、`internal/tools/mcptool/bridge.go`（`loadOne`）、`internal/mcpconfig/config.go`（`UsePrefix`）；judge 前置④（c_xujwvq）
