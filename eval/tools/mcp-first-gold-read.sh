#!/usr/bin/env bash
# mcp-first-gold-read.sh — offline first_gold_read for the
# mcp-semble-routing case group (docs/prd/feature-mcp-client.md §5.2:
# "第几个 tool_call 首次 read 到 gold 文件（离线 jq 算，不进 vocab）").
#
# Usage: mcp-first-gold-read.sh <traces-dir>
# Prints one TSV line per archived trace: case, first_gold_read
# (1-based index among ALL tool_start events of the first read whose
# args name the probe's gold file; "-" when the run never read it).
#
# Input = eval/tmp/mcp-semble-routing/traces/*.jsonl, archived by
# eval/tools/mcp-arm-runner.sh. eval/run.sh folds the raw stream into
# fixed vocab metrics then deletes it; tool_start ORDER is exactly the
# input first_gold_read needs, which is why it is deliberately NOT a
# vocab metric (PRD §5.2) and lives here instead.
set -u

dir=${1:?usage: mcp-first-gold-read.sh <traces-dir>}
[[ -d "$dir" ]] || { echo "mcp-first-gold-read: no such dir: $dir" >&2; exit 2; }

shopt -s nullglob
for f in "$dir"/*.jsonl; do
  base=$(basename "$f")
  # Strip the runner's -<UTC stamp>-<pid>.jsonl suffix.
  case_name=$(printf '%s\n' "$base" | sed -E 's/-[0-9]{8}T[0-9]{6}Z-[0-9]+\.jsonl$//')
  probe=${case_name##*-}
  case "$probe" in
    nl)     gold="internal/permission/permission.go" ;;
    symbol) gold="internal/sysprompt/sysprompt.go" ;;
    mixed)  gold="internal/tools/references/references.go" ;;
    *)      continue ;;
  esac
  idx=$(jq -rs --arg gold "$gold" '
    [ .[] | select(.type=="tool_start") ]
    | [ to_entries[]
        | select(.value.name=="read" and ((.value.args // "") | contains($gold)))
        | (.key + 1) ]
    | if length == 0 then "-" else (.[0] | tostring) end
  ' "$f")
  printf '%s\t%s\n' "$case_name" "$idx"
done
