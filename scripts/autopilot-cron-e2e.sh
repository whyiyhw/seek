#!/usr/bin/env bash
# autopilot-cron-e2e.sh — c_kdblc4 sandbox self-test (v8 §9.5③1).
#
# Full-chain proof that a due cron job really launches autopilot on the
# subscription default lane and the report lands in the §9.5③3 ledger,
# with FAKE keys only (§9.1 discipline: nobody watches this run, so
# nothing here may touch a real key or the real ~/.seek).
#
#   cron create --autopilot  →  NextRun due  →  seek cron tick
#     → child `seek autopilot run` (SEEK_AUTOPILOT_CRON=1 stamped)
#       → lane pin: opencode (fake key + local fake OpenAI server)
#         → decompose → worktree worker → judge → Report
#           → SEEK_HOME/autopilot/reports.jsonl  ← the §9.5③3 ledger
#
# Phase B proves the billing gate: a cron env overlay pinning the
# metered deepseek lane WITHOUT SEEK_AUTOPILOT_ALLOW_METERED makes the
# child fail fast with the refusal, and the ledger stays untouched.
#
# Isolation: everything lands under one mktemp dir (SEEK_HOME, goal
# repo, GOCACHE, server log). DEEPSEEK_API_KEY is set to a fake value
# ON PURPOSE — the run completing against the opencode fake server
# while that key sits in the env is exactly the proof that the cron
# pin wins over buildProvider's auto-detect (acceptance ②).
#
# Cost: ~4 min wall (two 70 s waits for @every 1m schedules to come
# due — the minimum seek accepts; we want the REAL schedule expiry,
# not a forced `cron run`).

set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d "${TMPDIR:-/tmp}/seek-autopilot-e2e.XXXXXX")
SEEK="$TMP/bin/seek"
FAILURES=0

note() { printf '[e2e] %s\n' "$*"; }
fail() { printf '[e2e] FAIL: %s\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }

[ -x "$(command -v python3)" ] || { echo "python3 required" >&2; exit 3; }

note "sandbox: $TMP"

# --- build -----------------------------------------------------------------
note "building seek…"
(cd "$ROOT" && go build -o "$SEEK" ./cmd/seek)

# --- fake OpenAI-compatible server ----------------------------------------
# Classifies each /chat/completions call by its system prompt:
#   planner  → one trivial task JSON   (autopilot decomposer)
#   verifier → all-pass verdict JSON   (autopilot judge)
#   else     → plain text              (worktree worker, no tool calls)
# Every request is logged (auth + model + class) for the lane proof.
cat > "$TMP/fake-server.py" <<'PYEOF'
import json, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG = open(sys.argv[2], "a", buffering=1)

def sse(content, model):
    body = json.dumps({"choices": [{"delta": {}, "finish_reason": "stop"}],
                       "usage": {"prompt_tokens": 10, "completion_tokens": 5}})
    return ("data: " + json.dumps({"choices": [{"delta": {"content": content},
                                                "finish_reason": None}]}) + "\n\n"
            + "data: " + body + "\n\n"
            + "data: [DONE]\n\n")

class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_POST(self):
        n = int(self.headers.get("content-length", 0))
        req = json.loads(self.rfile.read(n) or b"{}")
        sysmsg = " ".join(m.get("content", "") for m in req.get("messages", [])
                          if m.get("role") == "system")
        auth = self.headers.get("authorization", "")
        # The real OpenCode Go endpoint 400s with MissingSessionID when
        # x-opencode-session is absent (verified 2026-11-18). Enforce it
        # here too: without this the sandbox stays green while the
        # production run dies on its FIRST decompose call (the
        # c_kdblc4 rework-round defect).
        sid = self.headers.get("x-opencode-session", "")
        if not sid:
            body = json.dumps({"error": {"type": "MissingSessionID",
                                         "message": "x-opencode-session header is required"}}).encode()
            LOG.write(json.dumps({"class": "missing-session",
                                  "auth": auth, "session": sid,
                                  "model": req.get("model", "")}) + "\n")
            self.send_response(400)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if "planner for an autonomous" in sysmsg:
            cls = "decompose"
            reply = json.dumps([{"title": "greet",
                "prompt": "This is a sandbox chain test. Reply with the single word DONE immediately; do not use any tools.",
                "done_when": ["the worker replied"]}])
        elif "independent verifier" in sysmsg:
            cls = "judge"
            reply = json.dumps({"assertions": [{"index": 1, "verdict": "pass",
                                                "evidence": ["T1"], "note": "sandbox"}],
                                "overall_note": "sandbox fake"})
        else:
            cls = "worker"
            reply = "DONE"
        # `session` rides the log so the lane proof can assert the
        # header was present (and shared by decompose+judge) instead of
        # trusting that the adapter filled it.
        LOG.write(json.dumps({"class": cls, "auth": auth,
                              "session": sid,
                              "model": req.get("model", "")}) + "\n")
        out = sse(reply, req.get("model", "")).encode()
        self.send_response(200)
        self.send_header("content-type", "text/event-stream")
        self.send_header("content-length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

srv = ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H)
srv.serve_forever()
PYEOF

PORT=$(python3 -c "import socket; s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()")
python3 "$TMP/fake-server.py" "$PORT" "$TMP/server.log" &
SERVER_PID=$!
trap 'kill $SERVER_PID 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  python3 -c "import socket; socket.create_connection(('127.0.0.1', $PORT), 1).close()" 2>/dev/null && break
  sleep 0.2
done
note "fake server on :$PORT"

# --- sandbox env ------------------------------------------------------------
export SEEK_HOME="$TMP/seek-home"
unset SEEK_SESSIONS_DIR || true
export OPENCODE_API_KEY="sk-fake-opencode-e2e"
export OPENCODE_BASE_URL="http://127.0.0.1:$PORT/v1/chat/completions"
export DEEPSEEK_API_KEY="sk-fake-deepseek-MUST-NOT-BE-USED"
export GOCACHE="$TMP/gocache"
export GIT_AUTHOR_NAME=e2e GIT_AUTHOR_EMAIL=e2e@seek.test
export GIT_COMMITTER_NAME=e2e GIT_COMMITTER_EMAIL=e2e@seek.test

# --- goal repo --------------------------------------------------------------
GOAL="$TMP/goal-repo"
mkdir -p "$GOAL/greet"
git -C "$GOAL" init -q
cat > "$GOAL/go.mod" <<'EOF'
module e2e-goal

go 1.24
EOF
cat > "$GOAL/greet/greet.go" <<'EOF'
package greet

func Hello() string { return "hello" }
EOF
git -C "$GOAL" add -A
git -C "$GOAL" -c user.name=e2e -c user.email=e2e@seek.test commit -qm init

# --- phase A: positive full chain ------------------------------------------
note "phase A: register autopilot job (@every 1m)"
"$SEEK" cron create --name e2e-positive --at '@every 1m' --autopilot \
  --notify never --cwd "$GOAL" 'sandbox goal: prove the cron→autopilot chain'

note "waiting 70s for the schedule to come due (real expiry, not cron run)…"
sleep 70

note "tick"
"$SEEK" cron tick

RUNS_DIR="$SEEK_HOME/cron/runs"
REC=$(ls -t "$RUNS_DIR"/*.jsonl 2>/dev/null | grep -v lock | head -1 || true)
if [ -n "$REC" ]; then
  grep -q '"completed"' "$REC" \
    || fail "run record has no completed event: $REC"
  note "run record OK: $REC"
else
  fail "no run record under $RUNS_DIR"
fi

LEDGER="$SEEK_HOME/autopilot/reports.jsonl"
if [ -s "$LEDGER" ]; then
  if python3 - "$LEDGER" <<'PY'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
r = rows[-1]
assert r["model"] == "deepseek-v4.1-flash", f"lane not opencode default: {r['model']}"
assert r["verdict"] in ("pass", "doubt", "fail"), f"bad verdict: {r['verdict']}"
assert r["tasks"] >= 1 and r["goal"], f"row not self-describing: {r}"
print(f"[e2e] ledger row OK: verdict={r['verdict']} tasks={r['tasks']} model={r['model']}")
PY
  then :; else fail "ledger row invalid"; fi
  BEFORE_B=$(wc -l < "$LEDGER" | tr -d ' ')
else
  fail "ledger missing: $LEDGER"
  BEFORE_B=0
fi

# The ② proof: every LLM call went to the fake opencode server with the
# opencode fake key; the fake deepseek key was never presented anywhere.
if python3 - "$TMP/server.log" <<'PY'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
posts = [r for r in rows]
assert posts, "server saw no chat calls"
# The opencode lane's client contract: every call must carry
# x-opencode-session (the fake server now 400s without it, so a
# missing-session row would also break the chain — assert anyway so
# the failure names the cause instead of "no decompose call").
missing = [r for r in posts if not r.get("session")]
assert not missing, f"calls without x-opencode-session: {missing}"
bad = [r for r in posts if "sk-fake-deepseek" in r["auth"]]
assert not bad, f"deepseek key was used: {bad}"
assert all("sk-fake-opencode" in r["auth"] for r in posts), "non-opencode auth seen"
cls = [r["class"] for r in posts]
for want in ("decompose", "worker", "judge"):
    assert want in cls, f"chain incomplete, no {want} call: {cls}"
# decompose + judge are ONE conversation (the adapter mints the id
# once per run) — a per-call rotation would silently split the rerun's
# routing/cache affinity. Workers carry their own sub-sid, which is
# exactly why only these two are compared.
by_cls = {}
for r in posts:
    by_cls.setdefault(r["class"], []).append(r["session"])
sids = by_cls.get("decompose", []) + by_cls.get("judge", [])
assert len(set(sids)) == 1, f"decompose/judge session ids diverged (not one run): {by_cls}"
print(f"[e2e] lane proof OK: {len(posts)} calls {cls} — all opencode key; session header on every call, decompose+judge share one id")
PY
then :; else fail "lane proof failed (deepseek key used, missing session header, or chain incomplete)"; fi

"$SEEK" cron delete e2e-positive >/dev/null

# --- phase B: billing gate refusal -----------------------------------------
note "phase B: overlay pins metered deepseek lane with no approver"
mkdir -p "$SEEK_HOME/cron"
cat > "$SEEK_HOME/cron/env" <<'EOF'
SEEK_AUTOPILOT_PROVIDER=deepseek
EOF

"$SEEK" cron create --name e2e-gate --at '@every 1m' --autopilot \
  --notify never --cwd "$GOAL" 'gate refusal probe'
note "waiting 70s…"
sleep 70
"$SEEK" cron tick

GREC=$(ls -t "$RUNS_DIR"/*.jsonl 2>/dev/null | grep -v lock | head -1 || true)
if [ -n "$GREC" ]; then
  grep -q "metered" "$GREC" && grep -q "SEEK_AUTOPILOT_ALLOW_METERED" "$GREC" \
    || fail "gate refusal text not in run record: $GREC"
  note "gate refusal recorded: $GREC"
else
  fail "no run record for the gate job"
fi
AFTER_B=$( [ -f "$LEDGER" ] && wc -l < "$LEDGER" | tr -d ' ' || echo 0 )
if [ "$AFTER_B" != "$BEFORE_B" ]; then
  fail "ledger grew during the refused run ($BEFORE_B → $AFTER_B)"
else
  note "ledger untouched by the refused run ($AFTER_B rows)"
fi

"$SEEK" cron delete e2e-gate >/dev/null

# --- verdict ----------------------------------------------------------------
echo
if [ "$FAILURES" -eq 0 ]; then
  note "PASS — full chain + gate verified. Artifacts: $TMP"
  exit 0
fi
note "FAILURES: $FAILURES — inspect $TMP"
exit 1
