#!/usr/bin/env bash
# mcp-arm-runner.sh — three-arm seek invocation engine for the
# mcp-semble-routing case group (docs/prd/feature-mcp-client.md §5.1).
#
# Usage: mcp-arm-runner.sh <a1|a2|a3>
# Called from a case dir's runner.sh wrapper, under eval/run.sh's env
# contract (SEEK_EVAL_BINARY / SEEK_EVAL_REPO_ROOT /
# SEEK_EVAL_PROMPT_FILE / SEEK_EVAL_MAX_TURNS). One arm = ONE seek
# invocation = one JSONL stream; the case directory name carries the
# arm identity into the results row.
#
# The three arms run the SAME prompt against the SAME repo with only
# the environment differing:
#
#   a1  baseline — scratch SEEK_HOME with NO mcp.json: MCP tools are
#       never registered; measures the grep/read workflow ceiling.
#   a2  semble without guidance — mcp.json configures the semble
#       server; guidance switched off. Isolates the net effect of
#       guidance (the a3-vs-a2 delta).
#   a3  semble with guidance — same mcp.json, guidance at its default
#       (on). The full deep-integration shape.
#
# Arm isolation: each arm gets its own scratch SEEK_HOME under
# eval/tmp/mcp-semble-routing/<arm>/ so ONLY the mcp.json wiring
# differs between arms. Auth is copied from the real
# ~/.seek/config.json (DEEPSEEK_API_KEY is not exported in this
# environment — session-id-contract runner precedent). Invocation
# flags mirror the default harness (-yolo -no-save) so rows stay
# comparable with the rest of the suite; the process exit code
# surfaces through a runner_fact line (nonzero_exits metric).
#
# ROLLOUT CAVEAT (read before collecting a2/a3 data): the guidance
# switch ("mcp_prompt": false on the server entry) is D3 scope and is
# written server-level per the design's per-server copy model —
# RECONCILE the field placement with D3's landed mcpconfig schema
# before the a2 arm's real collection. Before D1 the bridge registers
# bare server-side tool names, so mcp_calls reads 0 in a2/a3 even when
# the model does call the tools — a2/a3 data is only valid after D1.
set -u

arm=${1:-}
case "$arm" in
  a1|a2|a3) ;;
  *) echo "mcp-arm-runner: arm must be a1|a2|a3, got '$arm'" >&2; exit 64 ;;
esac

scratch="$SEEK_EVAL_REPO_ROOT/eval/tmp/mcp-semble-routing/$arm"
home_dir="$scratch/home"
rm -rf "$scratch"
mkdir -p "$home_dir"
cp "$HOME/.seek/config.json" "$home_dir/config.json" 2>/dev/null || {
  echo "mcp-arm-runner: no ~/.seek/config.json to copy auth from — run will fail on API errors" >&2
}

if [[ "$arm" != "a1" ]]; then
  # Same semble entry for both MCP arms (PRD §三 Phase 0 shape,
  # verified against a running server 2026-11-16). a2 additionally
  # switches guidance off; a3 keeps the default. mcp_prompt is read
  # from D3 on; before that json.Unmarshal ignores it — which equals
  # "off", so a2 semantics hold at every point of the D1→D3 rollout.
  cat > "$home_dir/mcp.json" <<'EOF'
{
  "mcpServers": {
    "semble": {
      "command": "uvx",
      "args": ["--from", "semble[mcp]", "semble"]
    }
  }
}
EOF
  if [[ "$arm" == "a2" ]]; then
    jq '.mcpServers.semble.mcp_prompt = false' "$home_dir/mcp.json" \
      > "$home_dir/mcp.json.tmp" && mv "$home_dir/mcp.json.tmp" "$home_dir/mcp.json"
  fi
fi

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
out=$(mktemp)
SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" \
  -yolo -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt" \
  > "$out" 2>/dev/null
rc=$?

cat "$out"
printf '{"type":"runner_fact","arm":"%s","exit_code":%d}\n' "$arm" "$rc"
rm -f "$out"
