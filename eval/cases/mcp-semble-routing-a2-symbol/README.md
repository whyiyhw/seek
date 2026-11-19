# mcp-semble-routing: arm a2 / probe symbol

Three-arm comparison case for MCP tool routing (docs/prd/feature-mcp-client.md §5.1–§5.4, card MCP-D4).

- **Arm a2**: semble registered, guidance OFF (mcp_prompt:false per PRD §5.1)
- **Probe symbol**: exact-symbol locate — the ANTI-OVER-GUIDANCE case (grep should win; PRD §5.4 #3)
- Gold answer: internal/sysprompt/sysprompt.go — func ModeLabel — scored offline from final_text by `eval/tools/mcp-semble-score.sh`
- Engine: `eval/tools/mcp-arm-runner.sh` (arm isolation via scratch SEEK_HOME under eval/tmp/mcp-semble-routing/)
- Siblings: `mcp-semble-routing-{a1,a2,a3}-{nl,symbol,mixed}` — same prompt per probe, only the environment differs

DATA NOTE: D1 (`<server>__<tool>` naming) and D3 (top-level `mcp_prompt` key) are both in main; the 2026-11-18 collection ran on that tree, see the arm-gate note below.

## expect.json (landed at collection time)

The arm gate is lifted (D1/D3 landed in main): this case's `expect.json`
is checked in as of the 2026-11-18 three-arm collection, so the suite no
longer skips it. Bounds are the common set (`max_turns: 10`,
`exact_agent_ends: 1`, `max_nonzero_exits: 0`) plus the arm probe's own
bound — `min_mcp_calls: 1` on a3-nl, `max_mcp_calls: 0` on a3-symbol.
Collection results: `eval/results/mcp-semble-routing-three-arm-2026-11-18.md`.
