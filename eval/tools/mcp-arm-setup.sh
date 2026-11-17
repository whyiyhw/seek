#!/usr/bin/env bash
# mcp-arm-setup.sh — per-arm setup hook for the mcp-semble-routing
# case group. Invoked (from repo root, like every setup.sh) as
#   mcp-arm-setup.sh <a1|a2|a3>
#
# a1: nothing to prepare (no MCP server involved).
# a2/a3: prewarm the semble index ONCE so the first MEASURED query
# doesn't pay the one-time index build — that latency/turns cost is
# an indexing artifact, not a guidance effect (PRD §5.3). Semble has
# no eager "index" subcommand: indexes build on first search and are
# cached + invalidated on file change (README, verified 2026-11-17),
# so prewarm = one throwaway `semble search` over the repo, sharing
# the same uvx environment the MCP server will use.
#
# Best-effort by design: without uvx (or if the CLI shape drifted),
# print a notice and succeed — a cold index inflates a2/a3 elapsed_s,
# it does not invalidate the behavioral metrics. A marker file under
# eval/tmp/mcp-semble-routing/ keeps the prewarm to one pass per
# suite run (per case × per repeat invocation would re-run uvx 15×).
set -u

arm=${1:-}
[[ "$arm" == "a2" || "$arm" == "a3" ]] || exit 0

group_tmp="$PWD/eval/tmp/mcp-semble-routing"
marker="$group_tmp/.semble-prewarmed"
if [[ -f "$marker" ]]; then
  exit 0
fi
command -v uvx >/dev/null 2>&1 || {
  echo "mcp-arm-setup: uvx not found — skipping semble prewarm" >&2
  exit 0
}

mkdir -p "$group_tmp"
# Throwaway query: any hit set triggers the full index build. Output
# discarded; success only touches the marker.
if uvx --from 'semble[mcp]' semble search "permission policy" "$PWD" --top-k 3 >/dev/null 2>&1; then
  touch "$marker"
  echo "mcp-arm-setup: semble index prewarmed"
else
  echo "mcp-arm-setup: semble prewarm failed (non-fatal) — first a2/a3 query may pay the index build" >&2
fi
