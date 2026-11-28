### mcp.json 的键写错层级会被 encoding/json 静默忽略——A2 评估臂可无声等同 A3
- **Saw**: D4 三臂 runner 早期把 `"mcp_prompt": false` 写在 `mcpServers.semble` 里（设计稿的 per-server 形状）；loader 只读顶层 `Config.MCPPrompt`，`json.Unmarshal` 默认忽略未知键，a2 臂照样拿到完整 MCPSection——测出的「无引导」其实是「有引导」，指标照常产出。
- **Why**: Go `encoding/json` 对未知字段不报错；`mcp_prompt` 的落点（顶层）与直觉的 per-server 位置不同，写错不失败。
- **Fix**: `eval/tools/mcp-arm-runner.sh` 改写顶层键 + `jq -r '.mcp_prompt'` 断言为 `false`，不符即 `exit 65`；三臂首 turn prompt_tokens（A1 16066 / A2 17027 / A3 17165）成为隔离生效的实证（工具 schema +~960 tok、MCPSection +~138 tok）。Card `c_eyuxqm`。
- **Lesson**: 评估臂的环境开关必须在 runner 里断言「生效位置」——被静默忽略的配置键会让对照组与实验组悄悄合并，数据依旧整齐，结论全错。
- **Refs**: `internal/mcpconfig/config.go` `Config.MCPPrompt`；`eval/tools/mcp-arm-runner.sh`；`eval/results/mcp-semble-routing-three-arm-2026-11-18.md`。
