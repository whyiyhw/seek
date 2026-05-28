#!/usr/bin/env bash
# eval/run.sh — run one or all eval cases against a seek binary.
#
# Usage:
#   eval/run.sh                              # all cases, ./seek
#   eval/run.sh <case-name>                  # one case, ./seek
#   eval/run.sh <case-name> <binary-path>    # one case, named binary
#   eval/run.sh '' <binary-path>             # all cases, named binary
#
# Each case appends one JSON row to:
#   eval/results/<UTC-date>-<binary-version>.jsonl
#
# Exit code: 0 if every executed case passed, 1 if any failed, 2 on
# setup error (missing binary, unparseable expect.json, etc.).

set -u  # treat unset vars as errors; do NOT set -e — we want to
        # iterate every case even if one fails.

repo_root=$(cd "$(dirname "$0")/.." && pwd)
eval_root="$repo_root/eval"
cases_dir="$eval_root/cases"
results_dir="$eval_root/results"

case_filter=${1-}
binary=${2-$repo_root/seek}

if [[ ! -x "$binary" ]]; then
  echo "eval: binary not found or not executable: $binary" >&2
  echo "      (build it first: go build -o seek ./cmd/seek)" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1 && ! command -v jq.exe >/dev/null 2>&1; then
  echo "eval: jq is required but not installed" >&2
  exit 2
fi
# Resolve jq binary name (WSL needs jq.exe, Linux/macOS uses jq).
if command -v jq >/dev/null 2>&1; then
  JQ=jq
else
  JQ=jq.exe
fi

mkdir -p "$results_dir"

# Stamp the binary identity into the results filename so multi-binary
# runs land in distinct files. `seek -version` formats as "vX.Y.Z · hash"
# or "dev · hash+" — we keep just the hash so the filename stays safe.
version_full=$("$binary" -version 2>/dev/null || echo "unknown")
version_tag=$(echo "$version_full" | awk -F' · ' '{print $1}' | sed 's/[^A-Za-z0-9._-]/_/g')
version_rev=$(echo "$version_full" | awk -F' · ' '{print $2}' | sed 's/[^A-Za-z0-9._-]/_/g')
date_stamp=$(date -u +%Y-%m-%d)
results_file="$results_dir/${date_stamp}-${version_tag}-${version_rev}.jsonl"

echo "eval: binary  $binary ($version_full)"
echo "eval: results $results_file"
echo

# extract_metric NAME JSONL_PATH → integer
# Keep this table in sync with eval/README.md's "Metric vocabulary".
extract_metric() {
  local name=$1 path=$2
  case "$name" in
    unknown_field_errors)
      $JQ -s '[.[] | select(.type=="tool_end" and (.error // "" | contains("unknown field")))] | length' "$path"
      ;;
    think_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="think")] | length' "$path"
      ;;
    total_tool_calls)
      $JQ -s '[.[] | select(.type=="tool_start")] | length' "$path"
      ;;
    read_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="read")] | length' "$path"
      ;;
    grep_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="grep")] | length' "$path"
      ;;
    list_dir_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="list_dir")] | length' "$path"
      ;;
    bash_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="bash")] | length' "$path"
      ;;
    edit_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="edit")] | length' "$path"
      ;;
    git_calls)
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="git")] | length' "$path"
      ;;
    turns)
      $JQ -s '[.[] | select(.type=="turn_end")] | length' "$path"
      ;;
    *)
      echo "0"
      ;;
  esac
}

# run_one CASE_DIR → echoes a JSON row, sets pass_total / fail_total.
run_one() {
  local case_dir=$1
  local case_name
  case_name=$(basename "$case_dir")

  local prompt_file="$case_dir/prompt.txt"
  local expect_file="$case_dir/expect.json"
  if [[ ! -f "$prompt_file" ]] || [[ ! -f "$expect_file" ]]; then
    echo "eval: skipping $case_name (missing prompt.txt or expect.json)" >&2
    return
  fi

  local max_turns
  max_turns=$($JQ -r '.max_turns // 10' "$expect_file")
  local prompt
  prompt=$(cat "$prompt_file")

  local out_file
  out_file=$(mktemp -t "seek-eval-${case_name}.XXXXXX")
  local started_at
  started_at=$(date +%s)

  # -no-save: don't pollute ~/.seek/sessions/ with eval runs.
  # -yolo:    these prompts are read-only inspections; --yolo lets
  #           grep/read/list_dir run without inline approval prompts.
  # cwd:     repo root, so the agent has the actual seek codebase to
  #          inspect (most cases reference real files).
  ( cd "$repo_root" && "$binary" -yolo -no-save -max-turns "$max_turns" -json -p "$prompt" ) \
    > "$out_file" 2>/dev/null
  local elapsed=$(( $(date +%s) - started_at ))

  # Build a metrics object: extract one entry per metric in the
  # vocabulary. We compute the same fixed set every case so historical
  # diffs are comparable across cases.
  local metrics
  metrics=$($JQ -n \
    --argjson unknown_field_errors "$(extract_metric unknown_field_errors "$out_file")" \
    --argjson think_calls          "$(extract_metric think_calls          "$out_file")" \
    --argjson total_tool_calls     "$(extract_metric total_tool_calls     "$out_file")" \
    --argjson read_calls           "$(extract_metric read_calls           "$out_file")" \
    --argjson grep_calls           "$(extract_metric grep_calls           "$out_file")" \
    --argjson list_dir_calls       "$(extract_metric list_dir_calls       "$out_file")" \
    --argjson bash_calls           "$(extract_metric bash_calls           "$out_file")" \
    --argjson edit_calls           "$(extract_metric edit_calls           "$out_file")" \
    --argjson git_calls            "$(extract_metric git_calls            "$out_file")" \
    --argjson turns                "$(extract_metric turns                "$out_file")" \
    '{unknown_field_errors:$unknown_field_errors, think_calls:$think_calls, total_tool_calls:$total_tool_calls, read_calls:$read_calls, grep_calls:$grep_calls, list_dir_calls:$list_dir_calls, bash_calls:$bash_calls, edit_calls:$edit_calls, git_calls:$git_calls, turns:$turns}')

  # agent_end carries cumulative token counts; pull them straight out
  # so we can plot cost-vs-quality trends.
  local prompt_tokens completion_tokens
  prompt_tokens=$($JQ -s 'map(select(.type=="agent_end")) | .[0].prompt_tokens // 0' "$out_file")
  completion_tokens=$($JQ -s 'map(select(.type=="agent_end")) | .[0].completion_tokens // 0' "$out_file")

  # Check each bound from expect.json against the metric of the same
  # name. failures is an array of "metric: expected ≤/≥/= bound, got
  # actual" lines, empty when everything passed.
  local failures
  failures=$($JQ -r --argjson m "$metrics" '
    [ to_entries[]
      | select(.key | startswith("max_") or startswith("min_") or startswith("exact_"))
      | . as $entry
      | ($entry.key | sub("^(max|min|exact)_"; "")) as $metric
      | ($entry.key | capture("^(?<op>max|min|exact)_") | .op) as $op
      | ($m[$metric] // null) as $actual
      | if $actual == null then "(unknown metric \($metric))"
        elif $op=="max"   and $actual <= $entry.value then empty
        elif $op=="min"   and $actual >= $entry.value then empty
        elif $op=="exact" and $actual == $entry.value then empty
        else "\($metric): want \($op)=\($entry.value), got \($actual)"
        end
    ]
  ' "$expect_file")

  local pass="true"
  if [[ "$failures" != "[]" ]]; then
    pass="false"
  fi

  local row
  row=$($JQ -n \
    --arg case "$case_name" \
    --argjson pass "$pass" \
    --argjson metrics "$metrics" \
    --argjson prompt_tokens "$prompt_tokens" \
    --argjson completion_tokens "$completion_tokens" \
    --argjson elapsed_s "$elapsed" \
    --argjson failures "$failures" \
    --arg version "$version_full" \
    --arg ran_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{case:$case, pass:$pass, metrics:$metrics, prompt_tokens:$prompt_tokens, completion_tokens:$completion_tokens, elapsed_s:$elapsed_s, failures:$failures, binary_version:$version, ran_at:$ran_at}')

  echo "$row" >> "$results_file"

  if [[ "$pass" == "true" ]]; then
    pass_total=$((pass_total + 1))
    echo "PASS  $case_name  (${elapsed}s, $(echo "$metrics" | $JQ -c .))"
  else
    fail_total=$((fail_total + 1))
    echo "FAIL  $case_name  (${elapsed}s)"
    echo "$failures" | $JQ -r '.[]' | sed 's/^/      /'
    echo "      metrics: $(echo "$metrics" | $JQ -c .)"
  fi

  rm -f "$out_file"
}

pass_total=0
fail_total=0

if [[ -n "$case_filter" ]]; then
  case_dir="$cases_dir/$case_filter"
  if [[ ! -d "$case_dir" ]]; then
    echo "eval: no such case: $case_filter" >&2
    exit 2
  fi
  run_one "$case_dir"
else
  shopt -s nullglob
  for case_dir in "$cases_dir"/*/; do
    run_one "${case_dir%/}"
  done
fi

echo
echo "eval: $pass_total passed, $fail_total failed"
exit $(( fail_total > 0 ? 1 : 0 ))
