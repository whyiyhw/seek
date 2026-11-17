# mcp-semble-routing: arm a2 / probe symbol

Three-arm comparison case for MCP tool routing (docs/prd/feature-mcp-client.md §5.1–§5.4, card MCP-D4).

- **Arm a2**: semble registered, guidance OFF (mcp_prompt:false per PRD §5.1)
- **Probe symbol**: exact-symbol locate — the ANTI-OVER-GUIDANCE case (grep should win; PRD §5.4 #3)
- Gold answer: internal/sysprompt/sysprompt.go — func ModeLabel — scored offline from final_text by `eval/tools/mcp-semble-score.sh`
- Engine: `eval/tools/mcp-arm-runner.sh` (arm isolation via scratch SEEK_HOME under eval/tmp/mcp-semble-routing/)
- Siblings: `mcp-semble-routing-{a1,a2,a3}-{nl,symbol,mixed}` — same prompt per probe, only the environment differs

DATA CAVEAT: valid only after D1 (server__tool naming) — before it, mcp_calls reads 0 even when the model does call the tools; also reconcile the mcp_prompt field placement with D3's landed mcpconfig schema before collecting.

## expect.json — add at collection time (arm gate)

This case ships WITHOUT expect.json on purpose: the suite skips a case
without it (`eval/README.md` scaffold convention), and this arm's data is
invalid until its prerequisite lands (see DATA CAVEAT above). When the arm
becomes collectable, write exactly:

```json
{
  "description": "mcp-semble-routing arm a2 / probe symbol",
  "max_turns": 10,
  "exact_agent_ends": 1,
  "max_nonzero_exits": 0
}
```
