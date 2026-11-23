#!/bin/bash
# anchor-crossharness.sh — eval v3 A3: drive the SAME anchor tasks through
# ANOTHER harness (same model, same provider lane) and grade them with the
# SAME check.sh — the harness-isolated control for "seek 这个 harness 值
# 多少分".
#
#   eval/tools/anchor-crossharness.sh [--task anchor-<name>]...
#
# PRIVATE (#2575): comparison rows land in eval/anchor/ ONLY — never in
# eval/results/ (that dir is the trend data source; anchor-crossharness
# rows there would fabricate regressions in scripts/eval-trend.sh).
#
# Env:
#   CROSS_HARNESS_BIN     harness executable (default: opencode)
#   CROSS_HARNESS_MODEL   model id, MUST match the seek-side anchor run
#                         (default deepseek-v4.1-flash — the OpenCode Go
#                         lane default; run.sh's anchor lane rides it too)
#   CROSS_HARNESS_CMD     command template overriding the built-in one:
#                         a string where %PROMPT_FILE% and %MODEL% are
#                         substituted; executed with cwd = the task ws.
#                         Built-in (best-effort, FIRST USE NEEDS
#                         CALIBRATION — see README): run with --model
#                         and the prompt on argv, auto-approve on.
#
# Degrades loudly: when the harness binary is missing the script prints
# the manual protocol (eval/anchor/README.md) and exits 0 — absent is
# not failure, silent skipping is.
#
# macOS bash 3.2 compatible.
set -u
cd "$(dirname "$0")/../../" || exit 2

command -v jq >/dev/null 2>&1 || { echo "anchor-crossharness: jq required" >&2; exit 2; }

harness=${CROSS_HARNESS_BIN:-opencode}
model=${CROSS_HARNESS_MODEL:-deepseek-v4.1-flash}
cmd_template=${CROSS_HARNESS_CMD-} # set → used verbatim (%PROMPT_FILE%/%MODEL%)
# 文件名安全形（路径形 BIN 如 /tmp/fake-harness → tmp-fake-harness）
harness_tag=$(basename "$harness" | tr -c 'a-zA-Z0-9._-' '-' | sed -e 's/-$//')

outdir=eval/anchor
mkdir -p "$outdir"

tasks=()
while [ $# -gt 0 ]; do
  case "$1" in
    --task) tasks+=("$2"); shift 2 ;;
    --task=*) tasks+=("${1#--task=}"); shift ;;
    *) echo "anchor-crossharness: unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [ ${#tasks[@]} -eq 0 ]; then
  for d in eval/cases/anchor-*/; do
    tasks+=("$(basename "$d")")
  done
fi

if ! command -v "$harness" >/dev/null 2>&1; then
  echo "anchor-crossharness: '$harness' not found on PATH." >&2
  echo "  Falling back to the MANUAL protocol — see eval/anchor/README.md:" >&2
  echo "  per task: copy workspace/ to a scratch dir, run the harness there with" >&2
  echo "  prompt.txt verbatim (model: $model), then run check.sh inside the scratch." >&2
  echo "  Write the table to eval/anchor/cross-<date>-<harness>.md by hand." >&2
  exit 0
fi

utc=$(date -u +%Y-%m-%d)
rows="$outdir/cross-${utc}-${harness_tag}.jsonl"
: > "$rows"

for t in "${tasks[@]}"; do
  case_dir="eval/cases/$t"
  [ -d "$case_dir/workspace" ] || { echo "skip: $t (no workspace)" >&2; continue; }
  scratch="eval/tmp/cross-$t"
  rm -rf "$scratch"; mkdir -p "$scratch"
  cp -R "$case_dir/workspace/." "$scratch/"
  started=$(date +%s)
  if [ -n "$cmd_template" ]; then
    cmd=${cmd_template//%PROMPT_FILE%/$case_dir/prompt.txt}
    cmd=${cmd//%MODEL%/$model}
    ( cd "$scratch" && eval "$cmd" ) >/dev/null 2>&1
  else
    # Built-in shape: prompt read into an argument (kept simple on purpose;
    # calibrate CROSS_HARNESS_CMD on first real use).
    prompt=$(cat "$case_dir/prompt.txt")
    ( cd "$scratch" && "$harness" run --model "$model" "$prompt" ) >/dev/null 2>&1
  fi
  hrc=$?
  elapsed=$(( $(date +%s) - started ))
  # check.sh resolves its own dir from $0 (golden/ lives there); scratch
  # is always eval/tmp/cross-<t> so the relative path back is ../../cases/.
  # NB: capture rc BEFORE any assignment — a bare assignment is itself a
  # command and clobbers $? (this exact bug shipped solved=true for a
  # no-op harness in the first smoke).
  ( cd "$scratch" && bash "../../cases/$t/check.sh" ) >/dev/null 2>&1
  check_rc=$?
  solved=false
  [ "$check_rc" -eq 0 ] && solved=true
  jq -cn --arg task "$t" --arg harness "$harness" --arg model "$model" \
    --argjson solved "$solved" --argjson harness_exit "$hrc" \
    --argjson elapsed "$elapsed" --arg ran_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{task:$task, harness:$harness, model:$model, solved:$solved, harness_exit:$harness_exit, elapsed_s:$elapsed, ran_at:$ran_at}' >> "$rows"
  rm -rf "$scratch"
  printf '  %-34s solved=%-5s (%ss)\n' "$t" "$solved" "$elapsed" >&2
done

{
  echo "# cross-harness: $harness × $model — $utc"
  echo
  echo "PRIVATE (#2575) — never publish. Same tasks, same model, same lane as"
  echo "the seek-side anchor rows in eval/results/; only the harness differs."
  echo
  total=$(wc -l < "$rows" | tr -d ' ')
  solved=$(jq -s 'map(select(.solved)) | length' "$rows")
  echo "- solved: ${solved}/${total}"
  echo
  echo '| task | solved | harness_exit | elapsed_s |'
  echo '|---|---|---|---|'
  jq -r '"| \(.task) | \(.solved) | \(.harness_exit) | \(.elapsed_s) |"' "$rows"
} > "$outdir/cross-${utc}-${harness_tag}.md"

echo "anchor-crossharness: rows → $rows"
echo "anchor-crossharness: table → $outdir/cross-${utc}-${harness_tag}.md"
