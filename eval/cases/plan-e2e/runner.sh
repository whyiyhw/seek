#!/usr/bin/env bash
# runner.sh — plan-e2e (eval v2 E3, c_c5wwey). Replaces the default
# harness invocation: the plan toolchain's approval gate is
# interactive by design (propose asks through askuser.Policy, whose
# callback is wired ONLY on the TUI path — print mode has no picker),
# so this case drives the real TUI through a pty and answers the
# picker itself. eval/tools/plan-e2e-drive.py is the driver,
# eval/tools/fake_plan_endpoint.py the scripted model,
# eval/tools/plan-e2e-judge.py the scorer.
#
# Lane note: this case is endpoint-agnostic BY CONSTRUCTION — the
# "model" is a local scripted fixture (fake key, zero API spend), so
# the invocation pins -provider compatible at the fixture's port and
# deliberately does NOT forward ${SEEK_EVAL_PROVIDER_ARGS-}. Judging
# covers the TOOLCHAIN (markers, gate, transcript, resume fold), not
# model choice — see the case README for what that split means.
#
# Chain:  fresh lane  seek -plan (propose→approve_batch→plan(start)→
#                     write→plan(complete)→done text) → /exit
#         resume lane seek -resume <sid> (plan(complete,2) fold probe
#                     →done text) → /exit
# Both lanes share one SEEK_HOME and one workspace under eval/tmp/;
# the session file survives /exit (TUI auto-save), so the judge reads
# ONE jsonl whose lines past --fresh-lines are the resume segment.
#
# Env contract (eval/run.sh): SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT,
# SEEK_EVAL_PROMPT_FILE, SEEK_EVAL_MAX_TURNS.
set -u

scratch="$SEEK_EVAL_REPO_ROOT/eval/tmp/plan-e2e"
home_dir="$scratch/home"
ws="$scratch/ws"
rm -rf "$scratch"
mkdir -p "$home_dir" "$ws"

# Pick a free port for the fixture endpoint (parallel --rounds must
# not collide). Probe with python (stdlib) instead of /dev/tcp for
# macOS parity.
port=$(python3 - <<'EOF'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
EOF
)

endpoint_log="$scratch/endpoint.log"
PORT="$port" python3 "$SEEK_EVAL_REPO_ROOT/eval/tools/fake_plan_endpoint.py" \
  >"$endpoint_log" 2>&1 &
endpoint_pid=$!
trap 'kill "$endpoint_pid" 2>/dev/null; wait "$endpoint_pid" 2>/dev/null' EXIT

# Wait for the endpoint to accept a request (bounded retries).
ready=0
for _ in $(seq 1 40); do
  if curl -s -o /dev/null -m 2 -X POST "http://127.0.0.1:$port/v1/chat/completions" \
       -d '{"messages":[{"role":"user","content":"ping"}]}'; then
    ready=1
    break
  fi
  sleep 0.25
done
if [[ "$ready" != 1 ]]; then
  echo "plan-e2e runner: fixture endpoint never came up (port $port)" >&2
  # A silent early exit here would hand eval/run.sh an EMPTY stream —
  # every metric reads 0 and the max-bounds vacuously PASS. Every
  # early-out must therefore emit a failed fact so
  # runner_assert_failures (and nonzero_exits) turn the row red.
  printf '{"type":"runner_fact","assert":"runner_precondition","failed":true,"detail":"fixture endpoint never came up on port %s (log: %s)"}\n' "$port" "$endpoint_log"
  printf '{"type":"runner_fact","invocation":0,"exit_code":64}\n'
  exit 64
fi

base="http://127.0.0.1:$port/v1/chat/completions"
model="plan-e2e-fake"
seek_args=(-provider compatible -base-url "$base" -model "$model"
           -max-turns "$SEEK_EVAL_MAX_TURNS")

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
resume_prompt="RESUME-FOLD-CHECK: finish the remaining step."

drive="$SEEK_EVAL_REPO_ROOT/eval/tools/plan-e2e-drive.py"
judge="$SEEK_EVAL_REPO_ROOT/eval/tools/plan-e2e-judge.py"

# --- fresh lane ---------------------------------------------------------
# -plan arms the real defense for A3 (WorkflowPlanAnalyze is read-only
# even under PrefYolo); the picker answer selects "approve —
# auto-approve per step" so plan(start) arms the pre-approval gate
# and writes flow without per-call y/N the driver cannot answer.
fresh_status="$scratch/drive-fresh.json"
fresh_raw="$scratch/pty-fresh.bin"
(
  cd "$ws"
  export SEEK_HOME="$home_dir" OPENAI_API_KEY="fake-plan-e2e-key"
  python3 "$drive" --status "$fresh_status" --record "$fresh_raw" \
    --prompt "$prompt" --done PLAN-E2E-DONE --deadline 150 -- \
    "$SEEK_EVAL_BINARY" -plan "${seek_args[@]}"
) >/dev/null 2>&1
drive1_rc=$?
# If the driver itself crashed before writing its status file, plant
# an all-false one so the judge scores A9 red instead of dying on a
# missing file (a crashed judge emits no facts — vacuous pass).
if [[ ! -s "$fresh_status" ]]; then
  printf '{"prompt_sent":false,"approved":false,"done":false,"exited":false,"timed_out":true}' > "$fresh_status"
fi

# The session id: freshest file under SEEK_HOME/sessions (TUI saved it
# on /exit; a missing dir means the chain never got anywhere — the
# judge will score every assertion false via the status files).
session_file=""
if [[ -d "$home_dir/sessions" ]]; then
  session_file=$(ls -t "$home_dir/sessions"/*.jsonl 2>/dev/null | head -1 || true)
fi
if [[ -z "$session_file" ]]; then
  # Vacuous-pass guard, same doctrine as the endpoint branch above.
  printf '{"type":"runner_fact","assert":"runner_precondition","failed":true,"detail":"no session file after fresh lane"}\n'
  printf '{"type":"runner_fact","invocation":0,"exit_code":3}\n'
  exit 3
fi
fresh_lines=$(wc -l < "$session_file" | tr -d ' ')
sid=$(basename "$session_file" .jsonl)

# --- resume lane --------------------------------------------------------
resume_status="$scratch/drive-resume.json"
resume_raw="$scratch/pty-resume.bin"
(
  cd "$ws"
  export SEEK_HOME="$home_dir" OPENAI_API_KEY="fake-plan-e2e-key"
  python3 "$drive" --status "$resume_status" --record "$resume_raw" \
    --prompt "$resume_prompt" --done RESUME-E2E-DONE --no-approve \
    --deadline 150 -- \
    "$SEEK_EVAL_BINARY" -resume "$sid" "${seek_args[@]}"
) >/dev/null 2>&1
drive2_rc=$?
if [[ ! -s "$resume_status" ]]; then
  printf '{"prompt_sent":false,"approved":false,"done":false,"exited":false,"timed_out":true}' > "$resume_status"
fi

# Exactly one session file: -resume must REUSE the session, not fork a
# second one. Surfaced as a runner_fact so the row shows it.
session_count=$(ls "$home_dir/sessions"/*.jsonl 2>/dev/null | wc -l | tr -d ' ')
if [[ "$session_count" == 1 ]]; then
  printf '{"type":"runner_fact","assert":"resume_same_session","failed":false,"detail":"sid=%s"}\n' "$sid"
else
  printf '{"type":"runner_fact","assert":"resume_same_session","failed":true,"detail":"sessions dir holds %s files after resume (want 1)"}\n' "$session_count"
fi

# --- judging ------------------------------------------------------------
# The judge re-reads the transcript + workspace + drive statuses and
# emits one runner_fact per assertion; its stdout joins this runner's
# stream so eval/run.sh's runner_assert_failures metric can count it.
python3 "$judge" \
  --fresh "$session_file" \
  --fresh-lines "$fresh_lines" \
  --ws "$ws" \
  --status1 "$fresh_status" \
  --status2 "$resume_status"
judge_rc=$?

# Driver crashes (pty unavailable, usage) must not vanish under the
# judge's verdict — surface as runner_fact exit codes. The judge's own
# exit (count of failed assertions) rides the same channel so the
# nonzero_exits metric sees a judged break even if a future metric
# regression ever broke runner_assert_failures.
printf '{"type":"runner_fact","invocation":1,"exit_code":%d}\n' "$drive1_rc"
printf '{"type":"runner_fact","invocation":2,"exit_code":%d}\n' "$drive2_rc"
printf '{"type":"runner_fact","invocation":3,"exit_code":%d}\n' "$judge_rc"
