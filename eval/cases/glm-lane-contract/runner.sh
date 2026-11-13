#!/usr/bin/env bash
# runner.sh — glm-lane-contract. Replaces the default harness invocation
# (eval/run.sh) with a three-leg chain that locks the GLM subscription
# lane's contract as smoked against the real endpoint on 2026-11-12 and
# 2026-11-13 (butler events 11-12 18:09 / 11-13 01:29, PR #2897):
#
#   leg 1  fresh run    -json, session SAVED  → agent_end.session_id
#   leg 2  -resume sid  -json                 → agent_end carries
#           cache_hit_tokens ≥ 1 (the resumed run re-sends the whole
#           prefix, GLM paas/v4's server cache is warm — both real
#           smokes measured 10944 hit tokens exactly here)
#   leg 3  print mode   (no -json)            → stderr stats footer:
#           "model: glm…" and "est. cost: $…" that is NOT the exact
#           "$0" shape (runTurnLLM hit/miss buckets + glm-5.3 rate
#           card; runJSON prints NO footer, so the $ evidence only
#           exists in print mode)
#
# Provider-priority probe: every leg runs with garbage DEEPSEEK_API_KEY
# and ANTHROPIC_API_KEY in the env. config.KeyFor checks env BEFORE
# config, so the garbage beats even real keys stored in config.json —
# if --provider=glm ever stopped winning the flag race, auto-detect
# would hand a garbage key to DeepSeek/Anthropic, the run would die on
# auth (no agent_end, no footer), and the assertions below would fail.
# This is the 11-12 smoke's incidental finding: a stray
# ANTHROPIC_API_KEY in the shell hijacks auto-detect with a 401.
#
# Key handling (repo rule for real-API smokes): GLM_API_KEY comes from
# the environment or the repo-root .env (gitignored; sourced here,
# never placed on a command line) or ~/.seek/config.json
# providers.glm.api_key. No key anywhere → runner_fact exit_code=78 so
# the row FAILs loudly instead of vacuously passing. To exercise the
# plumbing without a key or the real endpoint, export
# SEEK_EVAL_GLM_BASE_URL pointing at a local fake paas/v4 server.
#
# Env contract (eval/run.sh): SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT,
# SEEK_EVAL_PROMPT_FILE, SEEK_EVAL_MAX_TURNS.
#
# bash-3.2 safe (macOS system bash): no associative arrays, and empty
# array expansion is guarded (`${arr[@]+"${arr[@]}"}` idiom) because
# set -u treats a bare empty-array expansion as an unbound variable
# before bash 4.4.
set -u

scratch="$SEEK_EVAL_REPO_ROOT/eval/tmp/glm-lane-contract"
home_dir="$scratch/home"
rm -rf "$scratch"
mkdir -p "$home_dir"
cp "$HOME/.seek/config.json" "$home_dir/config.json" 2>/dev/null || true

# Source the gitignored .env exactly like real-API smokes do — keys
# never appear on command lines or in this file.
if [[ -f "$SEEK_EVAL_REPO_ROOT/.env" ]]; then
  set -a
  . "$SEEK_EVAL_REPO_ROOT/.env"
  set +a
fi

facts=""
fact() { # fact EXIT_CODE NOTE — queue a runner_fact line for run.sh
  # The trailing newline lives OUTSIDE the command substitution —
  # $() strips trailing newlines, which would concatenate facts onto
  # one line; the stream contract is one JSON object per line.
  facts+="$(printf '{"type":"runner_fact","exit_code":%d,"note":"%s"}' "$1" "$2")"$'\n'
}

# Key gate: env (incl. .env) or the config.json copy inside SEEK_HOME.
key_in_cfg=$(jq -r '.providers.glm.api_key // empty' "$home_dir/config.json" 2>/dev/null || true)
if [[ -z "${GLM_API_KEY:-}" && -z "$key_in_cfg" ]]; then
  fact 78 "no GLM key: export GLM_API_KEY, add it to .env, or set providers.glm.api_key in ~/.seek/config.json"
  printf '%s' "$facts"
  exit 0
fi

# Provider-priority probe (see header comment).
export DEEPSEEK_API_KEY="glm-lane-contract-provider-priority-probe"
export ANTHROPIC_API_KEY="glm-lane-contract-provider-priority-probe"

# Optional endpoint override for hermetic plumbing shakedowns; unset on
# real runs so the pinned bigmodel.cn paas/v4 URL is exercised.
base_url_args=()
if [[ -n "${SEEK_EVAL_GLM_BASE_URL:-}" ]]; then
  base_url_args=(--base-url "$SEEK_EVAL_GLM_BASE_URL")
fi

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
run1=$(mktemp)
run2=$(mktemp)
run3err=$(mktemp)
trap 'rm -f "$run1" "$run2" "$run3err"' EXIT

# Leg 1: fresh -json run with the session saved (no -no-save) so a
# session_id exists for leg 2 to resume.
SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" --provider=glm \
  ${base_url_args[@]+"${base_url_args[@]}"} \
  -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt" > "$run1" 2>/dev/null
rc1=$?
fact "$rc1" "fresh -json run exit"

sid=$(jq -rs 'map(select(.type=="agent_end")) | .[0].session_id // empty' "$run1")

# Leg 2: resume the same session — the prefix re-send is what makes the
# provider's server cache warm; its agent_end must carry the hits.
rc2=0
if [[ -n "$sid" ]]; then
  SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" --provider=glm \
    ${base_url_args[@]+"${base_url_args[@]}"} \
    -resume "$sid" -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt" \
    > "$run2" 2>/dev/null
  rc2=$?
  fact "$rc2" "resume -json run exit"
else
  # Fresh run produced no session_id — the contract is already broken.
  # 87 mirrors session-id-contract's loud-failure marker.
  fact 87 "fresh run produced no agent_end.session_id; skipping resume leg"
fi

# Leg 3: print mode (no -json) — the ONLY mode whose stderr carries the
# stats footer with the pricing line. Asserts the two footer facts the
# real smokes read off-screen: which model served the run, and that the
# estimated cost is not the structurally-zero "$0" shape.
SEEK_HOME="$home_dir" "$SEEK_EVAL_BINARY" --provider=glm \
  ${base_url_args[@]+"${base_url_args[@]}"} \
  -yolo -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" -p "$prompt" \
  > /dev/null 2> "$run3err"
rc3=$?
fact "$rc3" "print-mode run exit"

model_line=$(grep -E '^model: ' "$run3err" | head -1)
if [[ -z "$model_line" ]]; then
  fact 3 "print-mode stats footer missing 'model:' line (no footer = run died before stats)"
elif [[ ! "$model_line" =~ ^model:[[:space:]]*glm ]]; then
  fact 3 "stats footer model is not glm*: $model_line — provider routing lost --provider=glm"
fi

cost_line=$(grep -E '^est\. cost: ' "$run3err" | head -1)
if [[ -z "$cost_line" ]]; then
  fact 4 "print-mode stats footer missing 'est. cost:' line"
elif [[ "$cost_line" =~ \$0([[:space:]]|$) ]]; then
  # FormatCost(0) renders exactly "$0"; every nonzero cost keeps at
  # least one nonzero digit ("$0.0007" does NOT match this pattern).
  fact 4 "est. cost is the exact-zero shape: $cost_line — hit/miss buckets or rate card regressed"
fi

cat "$run1" "$run2"
printf '%s' "$facts"
