#!/usr/bin/env bash
# runner.sh — compact-ab.
#
# WHY THIS SHAPE (read before changing it)
#
# `/compact` only runs inside the interactive TUI, and auto-compact is
# TUI-only by design (docs/guide-budget.md §2.5) — `seek -p` has no
# compact command. So the A/B cannot be "run compact, then run the
# follow-up". It is instead built from SESSION FILES that differ in
# exactly one respect:
#
#   arm A — the full conversation (4 messages, facts present)
#   arm B — the summary pair /compact leaves behind, produced by
#           handleCompactDone (internal/tui/update_agent.go):
#             user      "Here is a summary of our earlier conversation. …"
#             assistant "Understood — I have the context. Ready to continue."
#
# Both arms then get the SAME follow-up prompt against the SAME
# scripted endpoint, which answers extractively: it emits the three
# golden facts only when they are present in the request body, else
# NO-FACTS-AVAILABLE. That makes the case mechanical and zero-cost —
# an LLM judge would be grading this repo's own canned prose (see the
# endpoint's header comment).
#
# The negative leg is the point: with `--summary bad` the identical
# pipeline must FAIL both arms' fact assertions differently — arm A keeps
# its facts, arm B loses them. `scripts/eval-compact-ab-test.sh` drives
# that negative so this case cannot pass vacuously.
#
# Env contract (eval/run.sh): SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT,
# SEEK_EVAL_PROMPT_FILE, SEEK_EVAL_MAX_TURNS.
set -u

scratch="$SEEK_EVAL_REPO_ROOT/eval/tmp/compact-ab"
home_dir="$scratch/home"
ws="$scratch/ws"
rm -rf "$scratch"
mkdir -p "$home_dir" "$ws"

summary_variant="${COMPACT_AB_SUMMARY:-good}"

# Free port (parallel --rounds must not collide). Probe with python
# (stdlib) for macOS parity, same as plan-e2e's runner.
port=$(python3 - <<'EOF'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
EOF
)

endpoint="$SEEK_EVAL_REPO_ROOT/eval/tools/compact-ab-endpoint.py"
endpoint_log="$scratch/endpoint.log"
PORT="$port" python3 "$endpoint" >"$endpoint_log" 2>&1 &
endpoint_pid=$!
trap 'kill "$endpoint_pid" 2>/dev/null; wait "$endpoint_pid" 2>/dev/null' EXIT

ready=0
for _ in $(seq 1 40); do
  if curl -s -o /dev/null -m 2 -X POST "http://127.0.0.1:$port/v1/chat/completions" \
       -d '{}'; then
    ready=1
    break
  fi
  sleep 0.25
done
if [[ "$ready" != 1 ]]; then
  # Vacuous-pass guard (plan-e2e doctrine): an empty stream would make
  # every metric read 0 and the max-bounds pass silently.
  printf '{"type":"runner_fact","assert":"runner_precondition","failed":true,"detail":"fixture endpoint never came up on port %s (log: %s)"}\n' "$port" "$endpoint_log"
  printf '{"type":"runner_fact","invocation":0,"exit_code":64}\n'
  exit 64
fi

seeded=$(python3 "$SEEK_EVAL_REPO_ROOT/eval/tools/compact-ab-fixture.py" \
  --home "$home_dir" --cwd "$ws" --model "compact-ab-fake" \
  --arm-a "arm-a-full" --arm-b "arm-b-summary" --summary "$summary_variant")

arm_a=$(printf '%s' "$seeded" | jq -r '.arm_a')
arm_b=$(printf '%s' "$seeded" | jq -r '.arm_b')
if [[ ! -s "$arm_a" || ! -s "$arm_b" ]]; then
  printf '{"type":"runner_fact","assert":"runner_precondition","failed":true,"detail":"fixture seeding produced no session files (%s / %s)"}\n' "$arm_a" "$arm_b"
  printf '{"type":"runner_fact","invocation":0,"exit_code":65}\n'
  exit 65
fi

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
base="http://127.0.0.1:$port/v1/chat/completions"
seek_args=(-provider compatible -base-url "$base" -model "compact-ab-fake"
           -yolo -max-turns "$SEEK_EVAL_MAX_TURNS" -json)

run_arm() { # $1 = sid, $2 = out file
  ( cd "$ws" && SEEK_HOME="$home_dir" OPENAI_API_KEY="fake-compact-ab-key" \
      "$SEEK_EVAL_BINARY" "${seek_args[@]}" -resume "$1" -p "$prompt" ) > "$2" 2>/dev/null
}

out_a=$(mktemp)
out_b=$(mktemp)
# Snapshot arm B BEFORE the run: seek rewrites the session file on every
# saved run (system prompt re-materialised, new turns appended), so the
# post-run file is no longer the seeded shape. The structural assertion
# is about what was SEEDED, so it reads this copy.
arm_b_seeded=$(mktemp)
cp "$arm_b" "$arm_b_seeded"
run_arm "arm-a-full" "$out_a"; rc_a=$?
run_arm "arm-b-summary" "$out_b"; rc_b=$?

cat "$out_a" "$out_b"
printf '{"type":"runner_fact","invocation":1,"exit_code":%d}\n' "$rc_a"
printf '{"type":"runner_fact","invocation":2,"exit_code":%d}\n' "$rc_b"

# --- mechanical scoring -------------------------------------------------
# Two facts per arm, both extractive: (a) did the answer carry all three
# golden strings, (b) does the request the endpoint saw look like the
# expected history shape. Emitted as runner_fact lines so
# runner_assert_failures counts them (the same pattern plan-e2e uses).
python3 - "$out_a" "$out_b" "$summary_variant" <<'PY'
import json, os, sys

FACTS = ["internal/csvexport/export.go", "report.csv", "45s"]
NO_FACTS = "NO-FACTS-AVAILABLE"


def text_of(path):
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            if ev.get("type") == "text_delta" and not ev.get("reasoning"):
                out.append(ev.get("delta") or "")
    return "".join(out)


def fact(name, ok, detail):
    print(json.dumps({"type": "runner_fact", "assert": name,
                      "failed": (not ok), "detail": detail}))


arm_a_text = text_of(sys.argv[1])
arm_b_text = text_of(sys.argv[2])
variant = sys.argv[3]

# Arm A: full history — must ALWAYS recover every fact, in both variants.
# A red here means the harness (session load / prompt / endpoint) broke,
# not that compaction lost something.
missing_a = [f for f in FACTS if f not in arm_a_text]
fact("arm_a_keeps_facts", not missing_a,
     "missing=%r text=%r" % (missing_a, arm_a_text[:160]))

# Arm B: the compacted history. In the `good` variant the summary carries
# the facts and the answer must recover them; in the `bad` variant it
# cannot by construction, and the assertion INVERTS — a "recovered"
# answer there would mean the endpoint is feeding arm B something other
# than its history (the vacuous-pass shape this negative leg exists to
# rule out).
#
# NB: no "arms are distinguishable" assertion. Both arms answering the
# same three facts produces byte-identical text in the good variant —
# that is the extractive endpoint working as designed, not a harness
# fault. What distinguishes the arms is the HISTORY, which is asserted
# structurally below (arm B's file must be the summary pair).
b_has_facts = not [f for f in FACTS if f not in arm_b_text]
if variant == "good":
    fact("arm_b_keeps_facts", b_has_facts,
         "text=%r" % arm_b_text[:160])
else:
    fact("arm_b_negative_loses_facts", (not b_has_facts) and (NO_FACTS in arm_b_text),
         "text=%r (want %s)" % (arm_b_text[:160], NO_FACTS))
PY

# Structural assertion on the seeded arm B session: it must be the
# summary pair, not a copy of arm A's history. Without this the two arms
# could be accidentally identical and every text-level assertion above
# would still pass.
python3 - "$arm_b_seeded" "$summary_variant" <<'PY'
import json, sys

path, variant = sys.argv[1], sys.argv[2]
msgs = []
with open(path, encoding="utf-8") as f:
    lines = [l for l in f if l.strip()]
for line in lines[1:]:
    msgs.append(json.loads(line))

MARKER = "Here is a summary of our earlier conversation."
ok = (len(msgs) == 2
      and msgs[0].get("role") == "user"
      and (msgs[0].get("content") or "").startswith(MARKER)
      and msgs[1].get("role") == "assistant")
print(json.dumps({"type": "runner_fact", "assert": "arm_b_is_summary_pair",
                  "failed": (not ok),
                  "detail": "msgs=%d first=%r" % (len(msgs),
                                                  (msgs[0].get("content") or "")[:60])}))
PY


rm -f "$out_a" "$out_b" "$arm_b_seeded"
