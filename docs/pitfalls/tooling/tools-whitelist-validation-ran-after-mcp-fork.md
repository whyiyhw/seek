### `-tools nosuch` 先 fork 全部 MCP 子进程再报 exit 2——参数校验排在副作用之后

- **Saw**: 2026-11-24 实测：配了 butler MCP 的机器上 `seek --tools nosuch` 的日志是「fork → `mcp: loaded 9 tool(s)` → 才发现名字不存在」。同理 `--tools read` 也照样 fork 全部 server，白付进程与延迟（提示词只省 244 tok）。错误路径比正常路径更贵。
- **Why**: 唯一一次 `-tools` 校验在 `applyToolsWhitelist`，它天然在**最后一个 `reg.Add` 之后**——因为 MCP 工具名只有 `ListTools` 之后才存在。`mcpconfig.Load()` + `LoadServers()` 排在它前面，于是校验晚于它要拦的那个副作用。
- **Fix**: 把能在加载前判定的部分前移：`checkToolsWhitelistPreMCP`（`cmd/seek/tools_precheck.go`）在 `LoadServers` 之前跑，读同一份 `mcpconfig.Config` 判三分——已注册的内建名 / 之后才注册的内建名（`postMCPTBuiltInTools`）放行；**可证明没有 server 能提供的名字**当场 exit 2；其余（命中已配置 server 的 `<server>__` 前缀，或存在 `"prefix": false` 的 server 可注册任意裸名）**推迟**到加载后校验。同一份 stderr 措辞，`errToolsUsage` → exit 2。
- **Lesson**: 校验门要放在它保护的那个副作用**之前**，而不是「数据齐了之后最方便的位置」。代价是判定力下降——前置门只能拒绝「可证明不可能」的值，把「未知但可能成立」留到有数据时再判。这个缺口必须显式命名并用测试钉死（`TestCheckToolsWhitelistPreMCP_Table` 三分表 + E2E 「fork 计数用子进程 pid 文件取证，不信日志行」），否则下一个人会把前置门收紧成「未知即拒」，合法调用直接起不来。
- **Refs**: `cmd/seek/main.go`（`run()` 的 `mcpCfg` 加载段 + `applyToolsWhitelist`）、`cmd/seek/tools_precheck.go`、`cmd/seek/tools_precheck_test.go`、`internal/tools/mcptool/bridge.go`（`loadOne` 的命名策略 = 推迟规则的依据）、`internal/mcpconfig/config.go`（`UsePrefix`）；judge 前置④（卡 c_xujwvq）。
