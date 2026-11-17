# mcp-semble-routing: arm a3 / probe nl

Three-arm comparison case for MCP tool routing (docs/prd/feature-mcp-client.md §5.1–§5.4, card MCP-D4).

- **Arm a3**: semble registered, guidance ON (D3 MCPSection copy at its default)
- **Probe nl**: natural-language intent locate — the POSITIVE routing case (semantic search should win; PRD §5.4 #1/#2)
- Gold answer: internal/permission/permission.go — ErrDenied region — scored offline from final_text by `eval/tools/mcp-semble-score.sh`
- Engine: `eval/tools/mcp-arm-runner.sh` (arm isolation via scratch SEEK_HOME under eval/tmp/mcp-semble-routing/)
- Siblings: `mcp-semble-routing-{a1,a2,a3}-{nl,symbol,mixed}` — same prompt per probe, only the environment differs

DATA CAVEAT: valid only after D1 + D3 final guidance copy; per the D4 card, A3 is re-run after D3's copy is final (min_mcp_calls fails by construction until then).

## expect.json — add at collection time (arm gate)

This case ships WITHOUT expect.json on purpose: the suite skips a case
without it (`eval/README.md` scaffold convention), and this arm's data is
invalid until its prerequisite lands (see DATA CAVEAT above). When the arm
becomes collectable, write exactly:

```json
{
  "description": "mcp-semble-routing arm a3 / probe nl",
  "max_turns": 10,
  "exact_agent_ends": 1,
  "max_nonzero_exits": 0, "min_mcp_calls": 1
}
```
