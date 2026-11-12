#!/usr/bin/env bash
# runner.sh — session-id-contract. Replaces the default harness
# invocation (eval/run.sh), which passes -no-save — and -no-save means
# no session, hence no agent_end.session_id at all. This contract needs
# the opposite shape, two chained invocations sharing one session store:
#
#   run 1: seek -p --json            → agent_end.session_id = <sid>
#   run 2: seek -resume <sid> --json → agent_end.session_id = SAME sid
#
# Both streams are concatenated for the metric extractor; expect.json
# asserts agent_ends=2, session_id_missing=0, session_id_distinct=1.
#
# SEEK_HOME points at an isolated temp dir so the runs never touch the
# real ~/.seek/sessions/. The user's config.json is copied in because
# auth lives there (DEEPSEEK_API_KEY is not exported in this
# environment); the copy sits under gitignored eval/tmp/ and teardown.sh
# removes it.
#
# Env contract (eval/run.sh): SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT,
# SEEK_EVAL_PROMPT_FILE, SEEK_EVAL_MAX_TURNS.
set -u

scratch="$SEEK_EVAL_REPO_ROOT/eval/tmp/session-id-contract"
home_dir="$scratch/home"
rm -rf "$scratch"
mkdir -p "$home_dir"
cp "$HOME/.seek/config.json" "$home_dir/config.json" 2>/dev/null || true

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
run1=$(mktemp)
run2=$(mktemp)

SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" \
  -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt" > "$run1" 2>/dev/null
rc1=$?

sid=$(jq -rs 'map(select(.type=="agent_end")) | .[0].session_id // empty' "$run1")

rc2=0
if [[ -n "$sid" ]]; then
  SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" \
    -resume "$sid" -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt" > "$run2" 2>/dev/null
  rc2=$?
else
  # First run produced no session_id — the contract is already broken.
  # Report a non-zero exit (and skip run 2: nothing to resume) so the
  # row fails loudly instead of vacuously passing.
  rc2=87
fi

cat "$run1" "$run2"
printf '{"type":"runner_fact","invocation":1,"exit_code":%d}\n' "$rc1"
printf '{"type":"runner_fact","invocation":2,"exit_code":%d}\n' "$rc2"
rm -f "$run1" "$run2"
