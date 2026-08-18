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

# --- Windows/git-bash path compatibility --------------------------------
# A Windows-native jq.exe (WinGet's, resolved below when it is the only
# jq on PATH) cannot open MSYS paths: /tmp/... and /d/... are not
# Windows paths, so EVERY jq invocation on such a path fails — expect
# bounds read as garbage (max_turns becomes an error string), and the
# mktemp'd stream file is unreadable, so every metric extracts as 0 and
# seek itself exits instantly on the garbage -max-turns. Under MSYS,
# rewrite the path vocabulary into mixed drive-letter form (D:/...),
# which both Windows binaries and MSYS binaries open. WSL/Linux have no
# cygpath and are untouched. (Sibling of the b041d62 WSL+jq.exe pitfall:
# the same class of break, on the git-bash side the old comment assumed
# worked.)
if command -v cygpath >/dev/null 2>&1; then
  repo_root=$(cygpath -m "$repo_root")
  eval_root=$(cygpath -m "$eval_root")
  cases_dir=$(cygpath -m "$cases_dir")
  results_dir=$(cygpath -m "$results_dir")
fi

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
# Resolve jq. Prefer a native jq; the jq.exe fallback only works when
# the script runs under MSYS (git-bash) with Windows-visible paths —
# under WSL a Windows jq.exe cannot open /mnt/... or /tmp/... paths
# (install the Linux jq instead: sudo apt install jq). See
# docs/test-plan-read-tool.md §8.3.
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
    git_subcommand_dupes)
      # The git tool auto-fixes a duplicated subcommand (args[0] ==
      # subcommand) and prepends a note containing "repeated from args"
      # to the result — counting the notes counts the model's mistakes,
      # zero round trips sacrificed (see internal/tools/git/git.go and
      # pitfalls "a refusal the model does not learn from").
      $JQ -s '[.[] | select(.type=="tool_end" and .name=="git" and (.result // "" | contains("repeated from args")))] | length' "$path"
      ;;
    bash_chains)
      # A bash call whose command chains multiple commands (';' or '&&').
      # ';' is not even a separator on cmd.exe (the Windows shell), so a
      # POSIX-style chain fails wholesale ("ambiguous argument 'echo'");
      # '&&' is legal on cmd but still the chaining anti-pattern — the
      # target shape is one command per call, or parallel dedicated-tool
      # calls. '|' is deliberately NOT counted: pipelines and grep
      # alternations (--grep="a|b") are legitimate. Matches the raw args
      # JSON of the tool call (pre-exec), so a failed chain still counts.
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="bash" and ((.args // "") | test("[;]|&&")))] | length' "$path"
      ;;
    bash_git_calls)
      # A bash call that invokes git — the dedicated git tool covers
      # every read-only query (log/diff/status/show/...), so reaching for
      # bash skips the subcommand whitelist and the auto-fix. Matches git
      # as a COMMAND TOKEN: preceded by start/whitespace/quote/;/&/| and
      # followed by whitespace or end — deliberately NOT \bgit\b, which
      # would false-positive on ".git" in paths (cd .git, ls .git) and
      # "github.com" style strings. Catches "cd x && git log" chains too.
      # Mutating git (commit/push) legitimately belongs in bash, but this
      # eval's prompts are read-only, so 0 is the correct target there.
      $JQ -s '[.[] | select(.type=="tool_start" and .name=="bash" and ((.args // "") | test("(^|[;&|[:space:]\"])git([[:space:]]|$)")))] | length' "$path"
      ;;
    probe_reads)
      # A read that returned zero lines from a non-starting offset is a
      # probe past EOF — the model could not tell "exactly N lines" from
      # "more pages exist". (An empty-file read at offset=1 also emits
      # "0 lines" but lacks the "from line" fragment, so it is excluded.)
      $JQ -s '[.[] | select(.type=="tool_end" and .name=="read" and (.result // "" | contains("0 lines emitted")) and (.result // "" | contains("from line")))] | length' "$path"
      ;;
    write_refusals)
      # fsobserve guard refusals start with "write refused:" (see
      # internal/fsobserve/fsobserve.go Explain).
      $JQ -s '[.[] | select(.type=="tool_end" and .name=="write" and (.error // "" | contains("write refused")))] | length' "$path"
      ;;
    turns)
      $JQ -s '[.[] | select(.type=="turn_end")] | length' "$path"
      ;;
    review_line_refs)
      # Content proxy for "number of findings": count code-location
      # callouts in the assistant's text (concatenated text_delta deltas,
      # reasoning excluded) — both the prose form ("line 24" / "L24") and
      # the file:line form ("sizecache.go:24" → the ":24"). A thorough
      # review cites more specific locations than a terse one.
      # Deliberately does NOT count severity words (critical/high/low/…) —
      # those collide with the effort vocabulary the prompt echoes, which
      # would bias the low-effort case upward. Rough by design (a fix
      # snippet that quotes a slice like a[1:2] over-counts); pair it with
      # completion_tokens, which is the robust signal.
      $JQ -rs '
        ( [ .[] | select(.type=="text_delta" and ((.reasoning // false) | not)) | (.delta // "") ] | join("") )
        | [ match("(?i)\\bline\\s*\\.?\\s*\\d+|\\bL\\d+\\b|:\\d+\\b"; "g") ]
        | length
      ' "$path"
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

  # Optional per-case hooks, run from repo root in the same cwd as the
  # seek invocation: setup.sh prepares fixtures (e.g. copies them into
  # eval/tmp/<case>/ so the model can mutate them), teardown.sh cleans
  # up afterwards. Teardown runs even when the seek invocation failed.
  local setup_file="$case_dir/setup.sh"
  local teardown_file="$case_dir/teardown.sh"
  if [[ -f "$setup_file" ]]; then
    ( cd "$repo_root" && bash "$setup_file" )
  fi

  local out_file
  out_file=$(mktemp -t "seek-eval-${case_name}.XXXXXX")
  # Same jq.exe/MSYS compatibility as the path block up top: mktemp
  # yields /tmp/..., which a Windows-native jq cannot reopen later.
  if command -v cygpath >/dev/null 2>&1; then
    out_file=$(cygpath -m "$out_file")
  fi
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

  if [[ -f "$teardown_file" ]]; then
    ( cd "$repo_root" && bash "$teardown_file" )
  fi

  # agent_end carries cumulative token counts; pull them straight out
  # so we can plot cost-vs-quality trends AND bound them as metrics
  # (completion_tokens is a robust verbosity proxy — load-bearing for
  # the code-review-effort cases).
  local prompt_tokens completion_tokens cache_hit_tokens
  prompt_tokens=$($JQ -s 'map(select(.type=="agent_end")) | .[0].prompt_tokens // 0' "$out_file")
  completion_tokens=$($JQ -s 'map(select(.type=="agent_end")) | .[0].completion_tokens // 0' "$out_file")
  cache_hit_tokens=$($JQ -s 'map(select(.type=="agent_end")) | .[0].cache_hit_tokens // 0' "$out_file")

  # Final assistant text (all turns' text_delta joined, reasoning
  # excluded, capped) so offline gold-answer scoring works without
  # keeping the raw stream — e.g. "does the answer contain 45s".
  local final_text
  final_text=$($JQ -rs '
    ( [ .[] | select(.type=="text_delta" and ((.reasoning // false) | not)) | (.delta // "") ] | join("") )
    | .[0:2000]
  ' "$out_file")

  # Build a metrics object: extract one entry per metric in the
  # vocabulary. We compute the same fixed set every case so historical
  # diffs are comparable across cases. completion_tokens is folded in
  # here (not just recorded) so expect.json can bound it.
  local metrics
  metrics=$($JQ -n \
    --argjson unknown_field_errors "$(extract_metric unknown_field_errors "$out_file")" \
    --argjson think_calls          "$(extract_metric think_calls          "$out_file")" \
    --argjson total_tool_calls     "$(extract_metric total_tool_calls     "$out_file")" \
    --argjson read_calls           "$(extract_metric read_calls           "$out_file")" \
    --argjson grep_calls           "$(extract_metric grep_calls           "$out_file")" \
    --argjson list_dir_calls       "$(extract_metric list_dir_calls       "$out_file")" \
    --argjson bash_calls           "$(extract_metric bash_calls           "$out_file")" \
    --argjson bash_chains          "$(extract_metric bash_chains          "$out_file")" \
    --argjson bash_git_calls       "$(extract_metric bash_git_calls       "$out_file")" \
    --argjson edit_calls           "$(extract_metric edit_calls           "$out_file")" \
    --argjson git_calls            "$(extract_metric git_calls            "$out_file")" \
    --argjson git_subcommand_dupes "$(extract_metric git_subcommand_dupes "$out_file")" \
    --argjson probe_reads          "$(extract_metric probe_reads          "$out_file")" \
    --argjson write_refusals       "$(extract_metric write_refusals       "$out_file")" \
    --argjson turns                "$(extract_metric turns                "$out_file")" \
    --argjson review_line_refs     "$(extract_metric review_line_refs     "$out_file")" \
    --argjson completion_tokens    "$completion_tokens" \
    '{unknown_field_errors:$unknown_field_errors, think_calls:$think_calls, total_tool_calls:$total_tool_calls, read_calls:$read_calls, grep_calls:$grep_calls, list_dir_calls:$list_dir_calls, bash_calls:$bash_calls, bash_chains:$bash_chains, bash_git_calls:$bash_git_calls, edit_calls:$edit_calls, git_calls:$git_calls, git_subcommand_dupes:$git_subcommand_dupes, probe_reads:$probe_reads, write_refusals:$write_refusals, turns:$turns, review_line_refs:$review_line_refs, completion_tokens:$completion_tokens}')

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
  # -c: jq 1.7+ pretty-prints -n object output by default; results must
  # stay strict single-line JSONL.
  row=$($JQ -cn \
    --arg case "$case_name" \
    --argjson pass "$pass" \
    --argjson metrics "$metrics" \
    --argjson prompt_tokens "$prompt_tokens" \
    --argjson completion_tokens "$completion_tokens" \
    --argjson cache_hit_tokens "$cache_hit_tokens" \
    --argjson elapsed_s "$elapsed" \
    --argjson failures "$failures" \
    --arg version "$version_full" \
    --arg final_text "$final_text" \
    --arg ran_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{case:$case, pass:$pass, metrics:$metrics, prompt_tokens:$prompt_tokens, completion_tokens:$completion_tokens, cache_hit_tokens:$cache_hit_tokens, elapsed_s:$elapsed_s, failures:$failures, binary_version:$version, final_text:$final_text, ran_at:$ran_at}')

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
