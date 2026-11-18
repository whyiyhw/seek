#!/usr/bin/env bash
# eval/ablate.sh — v8 pillar T: bare-core vs full-toolset dual run.
# (docs/prd/v8.md §4 柱 T · T2, card c_w3vhcm; prerequisite: the -tools
# whitelist flag, v8·T1.)
#
# Runs every eval case TWICE through eval/run.sh:
#   full lane — default tool registry (all tools attached)
#   bare lane — -tools read,edit,write,bash,grep (the v8 kernel five)
#
# and prints (and saves) a per-case comparison table — pass, turns,
# tool_calls, prompt/completion tokens, cost — plus a TOTAL row.
#
# Usage:
#   eval/ablate.sh                              # all cases, ./seek
#   eval/ablate.sh <case-name>                  # one case, ./seek
#   eval/ablate.sh <case-name> <binary-path>    # one case, named binary
#   eval/ablate.sh '' <binary-path>             # all cases, named binary
#
# LANE ISOLATION — the load-bearing rule: BOTH lanes write to
# eval/ablation/{full,bare}/, NEVER to eval/results/. eval/results/ is
# the data source for scripts/eval-trend.sh regression reports;
# ablation rows there would fabricate regressions. run.sh is steered
# via its env knobs (see eval/README.md § Ablation):
#   SEEK_EVAL_RESULTS_DIR  — per-lane JSONL sink
#   SEEK_EVAL_EXTRA_ARGS   — appended seek args (bare lane only)
#
# runner.sh cases: the bare lane only differs from full if the runner
# forwards SEEK_EVAL_EXTRA_ARGS into its own invocations (run.sh passes
# it through in the env contract; no existing runner reads it yet) —
# such cases are listed in a footnote; read their F/B row as
# lane-invariant by construction.
#
# Cost column: deepseek-v4.1-flash card ($0.30/M cache-miss · $0.006/M
# cache-hit · $1.20/M output — internal/pricing standardRates; same
# formula as pricing.Cost). Result rows do not record the model, so
# both lanes are priced on the same card — on other lanes read the F/B
# ratio, not the absolute $.
#
# Output: the table goes to stdout AND to
# eval/ablation/<date>-<version>.md (unchecked-in by intent: commit the
# .md manually when a run is meant for docs/prd/v8.md §9). The per-lane
# JSONL rows stay in eval/ablation/{full,bare}/ for re-analysis.
#
# Exit codes: 0 = both lanes measured (per-case FAILs are ablation
# DATA, not an ablate failure — run.sh prints its own pass/fail
# summaries); 2 = setup error (missing binary/jq, empty lane).
#
# macOS bash 3.2 compatible: no mapfile, no associative arrays; plain
# strings instead of arrays throughout.

set -u  # treat unset vars as errors; do NOT set -e — a failing case
        # inside a lane is data, and run.sh already handles iteration.

repo_root=$(cd "$(dirname "$0")/.." && pwd)
eval_root="$repo_root/eval"
cases_dir="$eval_root/cases"
ablation_root="$eval_root/ablation"
BARE_TOOLS="read,edit,write,bash,grep"

# Default expansions use the :- form throughout (set-but-empty counts as
# unset) — bash 3.2 + set -u discipline, same family as the pitfalls.md
# "bash 3.2 under set -u" entry. `${1-}` would pass an explicitly-empty
# arg through, e.g. `ablate.sh '' ''` → binary="" instead of ./seek.
case_filter=${1:-}
binary=${2:-$repo_root/seek}

if [[ ! -x "$binary" ]]; then
  echo "ablate: binary not found or not executable: $binary" >&2
  echo "        (build it first: go build -o seek ./cmd/seek)" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "ablate: jq is required but not installed" >&2
  exit 2
fi

mkdir -p "$ablation_root/full" "$ablation_root/bare"

# lane LABEL SUBDIR [EXTRA_ARGS] — run eval/run.sh with its JSONL sink
# pointed at eval/ablation/<subdir>/, then set the global lane_jsonl to
# that lane's results file (newest *.jsonl in the sink dir: run.sh
# names it <date>-<version>.jsonl and appends on same-day reruns, so
# newest-by-mtime is the file this lane just wrote).
lane() {
  local label=$1 subdir=$2 extra=${3:-}
  echo
  echo "==================================================================="
  echo "ablate: lane $label  (results: $ablation_root/$subdir/)"
  echo "==================================================================="
  SEEK_EVAL_RESULTS_DIR="$ablation_root/$subdir" \
  SEEK_EVAL_EXTRA_ARGS="$extra" \
  bash "$eval_root/run.sh" "$case_filter" "$binary"
  local rc=$?
  if [[ $rc -eq 2 ]]; then
    echo "ablate: run.sh exited 2 in lane $label (setup error); aborting" >&2
    exit 2
  fi
  lane_jsonl=$(ls -t "$ablation_root/$subdir"/*.jsonl 2>/dev/null | head -1)
  if [[ -z "$lane_jsonl" ]]; then
    echo "ablate: lane $label wrote no results file" >&2
    exit 2
  fi
}

lane full full ""
full_jsonl=$lane_jsonl
lane bare bare "-tools $BARE_TOOLS"
bare_jsonl=$lane_jsonl

# Table file mirrors the full lane's JSONL stem (same binary both
# lanes, so the stems coincide; a midnight-crossing rerun just joins
# two stems by case name — the jq below handles that fine).
table_file="$ablation_root/$(basename "$full_jsonl" .jsonl).md"

# runner.sh cases get no -tools injection unless their runner opts in —
# collect them for the footnote so the table cannot be misread.
runner_cases=""
for d in "$cases_dir"/*/; do
  c=${d%/}
  c=${c##*/}
  if [[ -n "$case_filter" && "$c" != "$case_filter" ]]; then
    continue
  fi
  if [[ -f "$d/runner.sh" ]]; then
    runner_cases="$runner_cases $c"
  fi
done

version_full=$("$binary" -version 2>/dev/null || echo "unknown")

# ---- comparison table (jq does the join; bash only frames it) -------
# F = full lane, B = bare lane; cells are "F/B" pairs, "-" = the case
# has no row in that lane. Per case the LAST row wins (run.sh appends;
# a same-day rerun supersedes earlier rows), and the table spans every
# case present in the LANE FILES — a filtered rerun still shows the
# other cases' earlier rows; rm -rf eval/ablation/ for a clean slate.
# NB the -n: without it jq runs the filter once per stdin line — zero
# lines on stdin means the filter never runs, output is empty, and the
# exit code still says 0.
table=$(jq -n -r --slurpfile fullf "$full_jsonl" --slurpfile baref "$bare_jsonl" '
  # deepseek-v4.1-flash card — see header comment on absolute-$ caveat.
  0.30 as $miss | 0.006 as $hit | 1.20 as $out |

  def lastrow: group_by(.case) | map(last);
  def idx: lastrow | map({key: .case, value: .}) | from_entries;
  ($fullf | idx) as $F |
  ($baref | idx) as $B |
  ([ ($F|keys[]) ] + [ ($B|keys[]) ] | unique) as $cases |

  def num($v): if $v == null then "-" else ($v|tostring) end;
  def metric($row; $k):
    if $row == null then null else ($row.metrics[$k] // 0) end;
  def cost($row):
    if $row == null then null
    else (((($row.prompt_tokens // 0) - ($row.cache_hit_tokens // 0)) * $miss
         + ($row.cache_hit_tokens // 0) * $hit
         + ($row.completion_tokens // 0) * $out) / 1000000)
    end;
  def fmt4($v): if $v == null then "-"
    else (($v * 10000 | round) / 10000 | tostring) end;
  def pm($row): if $row == null then "-"
    elif $row.pass then "pass" else "FAIL" end;

  ([ $cases[] | select($F[.] != null) ] | map($F[.])) as $fs |
  ([ $cases[] | select($B[.] != null) ] | map($B[.])) as $bs |
  ([$fs[] | select(.pass)] | length) as $passF |
  ([$bs[] | select(.pass)] | length) as $passB |
  ([$fs[] | .metrics.turns // 0] | add // 0) as $turnsF |
  ([$bs[] | .metrics.turns // 0] | add // 0) as $turnsB |
  ([$fs[] | .metrics.total_tool_calls // 0] | add // 0) as $toolsF |
  ([$bs[] | .metrics.total_tool_calls // 0] | add // 0) as $toolsB |
  ([$fs[] | .prompt_tokens // 0] | add // 0) as $prF |
  ([$bs[] | .prompt_tokens // 0] | add // 0) as $prB |
  ([$fs[] | .completion_tokens // 0] | add // 0) as $coF |
  ([$bs[] | .completion_tokens // 0] | add // 0) as $coB |
  ([$fs[] | cost(.)] | add // 0) as $cF |
  ([$bs[] | cost(.)] | add // 0) as $cB |

  "| case | pass F/B | turns F/B | tool_calls F/B | prompt tok F/B | completion tok F/B | cost USD F/B |",
  "|---|---|---|---|---|---|---|",
  ( $cases[] as $c |
    ($F[$c]) as $f | ($B[$c]) as $b |
    "| \($c) | \(pm($f))/\(pm($b)) | \(num(metric($f;"turns")))/\(num(metric($b;"turns"))) | \(num(metric($f;"total_tool_calls")))/\(num(metric($b;"total_tool_calls"))) | \(num($f.prompt_tokens))/\(num($b.prompt_tokens)) | \(num($f.completion_tokens))/\(num($b.completion_tokens)) | \(fmt4(cost($f)))/\(fmt4(cost($b))) |"
  ),
  "| TOTAL | \($passF)/\($passB) | \($turnsF)/\($turnsB) | \($toolsF)/\($toolsB) | \($prF)/\($prB) | \($coF)/\($coB) | \(fmt4($cF))/\(fmt4($cB)) |",
  "",
  "- full lane: \($fs | length) cases, \($passF) pass · \($turnsF) turns · \($toolsF) tool_calls · \($prF) prompt tok · \($coF) completion tok · $\(($cF * 100 | round) / 100)",
  "- bare lane: \($bs | length) cases, \($passB) pass · \($turnsB) turns · \($toolsB) tool_calls · \($prB) prompt tok · \($coB) completion tok · $\(($cB * 100 | round) / 100)",
  "- bare/full cost ratio: \(if $cF > 0 then "\((($cB / $cF) * 100 | round))%" else "-" end) · pass delta (bare - full): \($passB - $passF)"
')
if [[ -z "$table" ]]; then
  # The silent-empty trap this guards: a jq invocation that never runs
  # (see the -n note above) or dies on malformed rows would otherwise
  # ship a header-only "table" with exit 0.
  echo "ablate: comparison table generation failed (jq produced no output)" >&2
  exit 2
fi

{
  echo "# Ablation: bare-core (-tools $BARE_TOOLS) vs full"
  echo
  echo "- binary: \`$binary\` ($version_full)"
  echo "- ran: $(date -u +%Y-%m-%dT%H:%M:%SZ) · cases: ${case_filter:-all}"
  echo "- full rows: \`$full_jsonl\`"
  echo "- bare rows: \`$bare_jsonl\`"
  echo "- cost card: deepseek-v4.1-flash (rows carry no model id; on"
  echo "  other lanes read the F/B ratio, not the absolute \$)"
  echo
  echo "$table"
  if [[ -n "$runner_cases" ]]; then
    echo
    echo "runner.sh cases (bare lane = full invocation shape unless the"
    echo "runner forwards SEEK_EVAL_EXTRA_ARGS; none do today — their F/B"
    echo "rows are lane-invariant by construction):$runner_cases"
  fi
} | tee "$table_file"

echo
echo "ablate: table saved to $table_file"
exit 0
