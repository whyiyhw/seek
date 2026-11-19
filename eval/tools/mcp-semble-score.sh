#!/usr/bin/env bash
# mcp-semble-score.sh — offline gold_hit scorer for the
# mcp-semble-routing case group (docs/prd/feature-mcp-client.md §5.2:
# gold_hit = the final answer cites the gold file with a line inside
# the gold window; judged from the final_text column already checked
# into eval/results/*.jsonl — script-judged, human-spot-checked).
#
# Usage: mcp-semble-score.sh <results.jsonl>
# Prints one TSV line per mcp-semble-routing row: case, gold_hit(0|1),
# first gold-matching citation (or "-").
#
# Gold anchors are resolved DYNAMICALLY from the worktree at score
# time (grep line numbers of the defining constructs), so line drift
# from future commits self-heals instead of silently mis-scoring.
# Assumption: scoring happens against (approximately) the tree the
# rows were collected on — each collection round regenerates its
# summary, which holds in practice. Pair with a human spot-check of
# final_text, per PRD §5.2.
set -u

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
results=${1:?usage: mcp-semble-score.sh <results.jsonl>}
[[ -f "$results" ]] || { echo "mcp-semble-score: no such file: $results" >&2; exit 2; }

f_perm="internal/permission/permission.go"
f_mode="internal/sysprompt/sysprompt.go"
f_ref="internal/tools/references/references.go"

perm=$(grep -n "^var ErrDenied" "$repo_root/$f_perm" | head -1 | cut -d: -f1)
mode=$(grep -n "^func ModeLabel" "$repo_root/$f_mode" | head -1 | cut -d: -f1)
desc=$(grep -n "^const description" "$repo_root/$f_ref" | head -1 | cut -d: -f1)
degr=$(grep -n "^func degradeMessage" "$repo_root/$f_ref" | head -1 | cut -d: -f1)
for v in perm mode desc degr; do
  if [[ -z "${!v}" ]]; then
    echo "mcp-semble-score: gold anchor '$v' not found in the tree — resolution drifted, fix the grep pattern" >&2
    exit 2
  fi
done

jq -r \
  --arg perm_f "$f_perm" --argjson perm "$perm" \
  --arg mode_f "$f_mode" --argjson mode "$mode" \
  --arg ref_f "$f_ref" --argjson desc "$desc" --argjson degr "$degr" '
  def windows($p):
    if $p == "nl"     then [{f:$perm_f, lo:($perm-10), hi:($perm+25)}]
    elif $p == "symbol" then [{f:$mode_f, lo:($mode-6), hi:($mode+6)}]
    else [{f:$ref_f, lo:$desc, hi:($desc+8)}, {f:$ref_f, lo:($degr-8), hi:($degr+12)}]
    end;
  select(.case | startswith("mcp-semble-routing-"))
  | (.case | capture("mcp-semble-routing-a[123]-(?<probe>nl|symbol|mixed)$").probe) as $probe
  | windows($probe) as $w
  | [ (.final_text // "") | scan("([A-Za-z0-9_./-]+\\.go):([0-9]+)") ] as $cites
  | ( [ $cites[] | . as [$p, $l] | ($l | tonumber) as $n
        | $w[] | . as $win
        | select(($p | endswith($win.f)) and ($n >= $win.lo) and ($n <= $win.hi))
        | "\($p):\($l)" ] | first // "-" ) as $hit
  | [ .case, (if $hit == "-" then "0" else "1" end), $hit ] | @tsv
' "$results"
