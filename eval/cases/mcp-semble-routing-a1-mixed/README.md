# mcp-semble-routing: arm a1 / probe mixed

Three-arm comparison case for MCP tool routing (docs/prd/feature-mcp-client.md §5.1–§5.4, card MCP-D4).

- **Arm a1**: no mcp.json in the scratch SEEK_HOME — MCP never registered (baseline: the grep/read workflow ceiling)
- **Probe mixed**: mixed intent — natural routing distribution observed, no directional bound
- Gold answer: internal/tools/references/references.go — description const / degradeMessage — scored offline from final_text by `eval/tools/mcp-semble-score.sh`
- Engine: `eval/tools/mcp-arm-runner.sh` (arm isolation via scratch SEEK_HOME under eval/tmp/mcp-semble-routing/)
- Siblings: `mcp-semble-routing-{a1,a2,a3}-{nl,symbol,mixed}` — same prompt per probe, only the environment differs

Arm valid on any tree: no MCP surface involved.
