### 同一份 mcp.json 两次启动可能注册出不同的工具名
- **Saw**: chapter-12 §12.6 埋过的雷：旧「仅冲突时前缀」策略下，裸名归谁取决于哪个 server 先被遍历到（Go map 迭代序随机），同一份配置两次启动的注册名/顺序都可能不同。
- **Why**: `LoadServers` 直接按 map 迭代序遍历 `mcpServers`，注册名成为迭代序的函数。
- **Fix**: D1（`docs/prd/feature-mcp-client.md` §4.1）：一律默认 `<server>__<tool>` 前缀 + 按 server name 字典序遍历；`TestLoadServers_DeterministicRoster` 锁死「同配置两次注册名单逐字节一致」。Commit with card `c_4ejhhs`。
- **Lesson**: 凡是名字/名单从 map 迭代派生、又要给用户或 LLM 看的，必须先排序——否则配置不是契约，哈希种子才是。
- **Refs**: `internal/tools/mcptool/bridge.go`、`docs/book/chapter-12.md` §12.6。
