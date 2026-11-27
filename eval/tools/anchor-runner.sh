#!/bin/bash
# anchor-runner.sh — eval v3 A1: the shared runner for anchor-* cases
# (Terminal-Bench-lite private anchor, docs/prd/archive/feature-eval-v3.md §2.2).
#
# Each eval/cases/anchor-<name>/runner.sh is a two-line shim delegating
# here, so the isolation mechanics live in exactly one place. The shape
# follows plan-e2e's runner: private scratch under eval/tmp/<case>/ with
# its OWN SEEK_HOME (deeper isolation than run.sh's shared sandbox — a
# task that pokes user state must not poison the next round), the
# workspace/ fixture copied into ws/, and the seek invocation run with
# cwd = ws so the agent's whole world is the task.
#
# Contract (inherited from eval/run.sh): env SEEK_EVAL_BINARY /
# SEEK_EVAL_REPO_ROOT / SEEK_EVAL_PROMPT_FILE / SEEK_EVAL_MAX_TURNS /
# SEEK_EVAL_EXTRA_ARGS / SEEK_EVAL_PROVIDER_ARGS; JSONL event stream on
# stdout. Judging is mechanical: check.sh (exit 0 = solved) runs inside
# ws after the agent is done; an unsolved task emits a failed
# runner_fact which the existing max_runner_assert_failures bound gates.
# The SUT exit code rides the nonzero_exits channel (crash = red round).
#
# macOS bash 3.2 compatible.
set -u

# Shims pass their own dir (eval/cases/anchor-<name>) via env.
case_dir=${SEEK_EVAL_ANCHOR_DIR:-}
if [ -z "$case_dir" ] || [ ! -d "$case_dir/workspace" ]; then
  echo "anchor-runner: SEEK_EVAL_ANCHOR_DIR must name an anchor case dir (with workspace/)" >&2
  exit 2
fi

repo_root=$SEEK_EVAL_REPO_ROOT
scratch="$repo_root/eval/tmp/$(basename "$case_dir")"
home_dir="$scratch/home"
ws="$scratch/ws"
rm -rf "$scratch"
mkdir -p "$home_dir" "$ws"
cp -R "$case_dir/workspace/." "$ws/"

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")

# The agent runs INSIDE ws: read/edit/write/bash/grep all anchor there.
# Provider lane is forwarded exactly like the mcp-arm runners
# (${SEEK_EVAL_PROVIDER_ARGS-} empty only on the explicit deepseek
# fallback); credential env rides along from run.sh's exports.
( cd "$ws" && SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" \
    -yolo -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" -json \
    ${SEEK_EVAL_EXTRA_ARGS-} ${SEEK_EVAL_PROVIDER_ARGS-} \
    -p "$prompt" )
sut_rc=$?

# Mechanical verdict — from OUTSIDE the agent's world, so a "fixed" ws
# is judged by recomputation, never by the agent's claims. check.sh's
# STDOUT IS REDIRECTED: this runner's stdout is the metric stream
# (pitfall: a check that diffs to stdout poisons every downstream jq -s
# — anchor-debug-tz shipped exactly that, one empty-metrics row per
# lane run); failure detail rides the failed fact below, stderr is
# already discarded by run.sh's runner capture.
( cd "$ws" && bash "$case_dir/check.sh" ) >/dev/null
check_rc=$?

[ "$sut_rc" -eq 0 ] || printf '{"type":"runner_fact","exit_code":%d}\n' "$sut_rc"
if [ "$check_rc" -ne 0 ]; then
  printf '{"type":"runner_fact","assert":"anchor_solved","failed":true,"detail":"check.sh exited %d"}\n' "$check_rc"
fi
exit 0
