#!/usr/bin/env bash
#
# acp-smoke-test.sh — real-binary ndJson smoke for `seek acp` (lane-runnable).
#
# WHY A SCRIPT AND NOT A MANUAL PIPE (card c_i2cyb7, option B of c_bnjrw5)
#
# The verify lane's command discipline forbids pipes/redirections, so the
# one acceptance check that proves the REAL binary speaks ACP over stdio
# could never run in-lane — c_bnjrw5 item 7 was rejected exactly there
# ("无法向二进制 stdin 送 ndJson"), and every re-verification fell back
# to a human hand-running the pipe. Wrapping the pipe inside this script
# moves it behind the selftest entry (`bash scripts/selftest.sh
# acp-smoke-test`; discovery-based since c_aqbdaz — scripts/*.sh are
# covered with zero registration), which the lane whitelist does approve.
#
# WHAT IT COVERS (and what it deliberately does NOT)
#
#   covered   build → spawn real `seek acp` → newline-JSON-RPC handshake
#             (initialize) → session/new with a cwd that differs from the
#             process launch dir → assert the D-1 double anchor actually
#             moved (sessionId is a durable transcript id, stderr anchor
#             line carries the absolute cwd + 16-hex project id) → bad cwd
#             fails loud with a JSON-RPC error → stdin EOF → clean exit rc=0.
#   covered   context-window frames (card c_uzlczr, P0-4) in TWO states —
#             low usage and near-full — against a local fake GLM endpoint
#             (eval/tools/fake_paas_glm.py, stdlib python3; reused, no new
#             fixture): each state asserts one `usage_update` arrives as a
#             session/update with ACP's own used/size fields, seek's
#             percent/severity under _meta, and (near-full only) the
#             threshold alert frame. Numbers are cross-checked against
#             budget.Limit(glm-5.3) = 1M, so the assertion is on the
#             same-source reading the TUI/Web meters use, not on a copy.
#   NOT       a real LLM round-trip. The prompt path is driven against a
#             loopback fake, so let alone keys/network nothing here can
#             cost money or flake on a model. Behaviour beyond the wire
#             shape is covered in-process by internal/acp + cmd/seek
#             tests (fake backends, no process spawn).
#
# Marker on success: ACP-NDJSON-SMOKE-OK (echoes the D1-SMOKE-OK lineage
# from c_bnjrw5's hand-run smoke).
#
# Usage: scripts/acp-smoke-test.sh   (exit 0 = all green, 1 = failure)
# Env:   none required; everything is built under a mktemp sandbox that
#        is removed on exit. Bash 3.2 compatible (no coproc, no mapfile).
#        Needs go + python3 (the fake context-window endpoint; precedent:
#        transcript-stats-test also pins python3).

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
  echo "acp-smoke: FAIL: $1" >&2
  [ -f "$SMOKE_ERR" ] && tail -5 "$SMOKE_ERR" >&2
  exit 1
}

# --- sandbox -----------------------------------------------------------------
# Everything seek could possibly touch lives under one mktemp dir: SEEK_HOME
# (so the real ~/.seek is never read or written), the launch dir, the anchor
# workspace, the fifo, and the captured streams. NOTE: macOS TMPDIR ends in
# a slash — strip it, or the sandbox path carries "//" while seek's
# filepath.Abs cleans it, and every string comparison against the anchor
# line (cwd=...) misses by one character.
TMPBASE="${TMPDIR:-/tmp}"; TMPBASE="${TMPBASE%/}"
SANDBOX="$(mktemp -d "$TMPBASE/acp-smoke.XXXXXX")" || exit 1
FAKEPID=""
trap ' [ -n "${SEEKPID:-}" ] && kill "${SEEKPID}" 2>/dev/null; [ -n "$FAKEPID" ] && kill "$FAKEPID" 2>/dev/null; rm -rf "$SANDBOX" ' EXIT
SMOKE_HOME="$SANDBOX/home"; mkdir -p "$SMOKE_HOME"
LAUNCH="$SANDBOX/launch";    mkdir -p "$LAUNCH"
WS="$SANDBOX/smoke-ws";      mkdir -p "$WS"
FIFO="$SANDBOX/in.fifo"; mkfifo "$FIFO"
OUT="$SANDBOX/acp-out.ndjson"
SMOKE_ERR="$SANDBOX/acp-stderr.log"
SEEKPID=""

echo "acp-smoke: sandbox=$SANDBOX"

# --- build the real binary ----------------------------------------------------
# Same module, same flags the docs use; output inside the sandbox only.
echo "acp-smoke: building ./cmd/seek ..."
(cd "$ROOT" && go build -o "$SANDBOX/seek" ./cmd/seek) || fail "go build failed"
[ -x "$SANDBOX/seek" ] || fail "built binary missing"

# --- spawn `seek acp` from LAUNCH (≠ anchor dir — that is the point of D-1) --
# DUMMY key: startup resolves the provider before the ACP server starts; no
# request ever leaves the process. Empty-string env vars neutralise any
# second-tier keys leaking in from the caller's environment (pitfall:
# "无头 ACP 车道 provider 只能经 env/config 选择" — here we pin, not probe).
cd "$LAUNCH" || fail "cd launch dir"
env SEEK_HOME="$SMOKE_HOME" \
    DEEPSEEK_API_KEY=selftest-dummy \
    ANTHROPIC_API_KEY= OPENAI_API_KEY= GEMINI_API_KEY= GLM_API_KEY= \
    "$SANDBOX/seek" acp < "$FIFO" > "$OUT" 2> "$SMOKE_ERR" &
SEEKPID=$!
exec 3> "$FIFO"   # hold the writer end open across requests

# wait_for <pattern> <label> <timeout_sec> — poll OUT until pattern appears.
# Ticks are integer 0.2s sleeps (bash 3.2: no floating-point arithmetic, no bc).
wait_for() {
  local pattern="$1" label="$2" timeout="$3" ticks=0
  local max_ticks=$((timeout * 5))
  while ! grep -q "$pattern" "$OUT" 2>/dev/null; do
    kill -0 "$SEEKPID" 2>/dev/null || fail "$label: seek acp died early"
    [ "$ticks" -ge "$max_ticks" ] && fail "$label: no response within ${timeout}s"
    sleep 0.2; ticks=$((ticks + 1))
  done
}

# rpc <id> <params-json> — one newline-JSON-RPC request line.
rpc() { echo "{\"jsonrpc\":\"2.0\",\"id\":$1,\"method\":\"$2\",\"params\":$3}" >&3; }

# --- 1. initialize handshake --------------------------------------------------
rpc 1 initialize '{"protocolVersion":1}'
wait_for '"id":1' 'initialize' 30
grep -q '"protocolVersion":1'        "$OUT" || fail 'initialize: protocolVersion != 1'
grep -q '"agentCapabilities"'        "$OUT" || fail 'initialize: no agentCapabilities'
grep -q '"image":true'               "$OUT" || fail 'initialize: promptCapabilities.image not advertised'
echo "acp-smoke: initialize ok (protocolVersion=1, image capability advertised)"

# --- 2. session/new anchors cwd (D-1 double anchor) ---------------------------
rpc 2 session/new "{\"cwd\":\"$WS\"}"
wait_for '"id":2' 'session/new' 15
# sessionId is the DURABLE transcript id since session/load shipped
# (YYYYMMDD-HHMMSS-16hex), not the project basename it used to be — the
# assertion here went stale with that change and this script's exit status
# with it (c_uzlczr found it red on a clean tree). The id's SHAPE is what
# matters to a client that has to hand it back to session/load.
python3 - "$OUT" <<'PY' || fail 'session/new: sessionId is not a durable transcript id (want YYYYMMDD-HHMMSS-16hex)'
import json, re, sys
ids = [json.loads(l)["result"]["sessionId"] for l in open(sys.argv[1])
       if '"id":2' in l and '"sessionId"' in l]
if not ids:
    print("no session/new result", file=sys.stderr); sys.exit(1)
if not re.fullmatch(r"\d{8}-\d{6}-[0-9a-f]{16}", ids[-1]):
    print(f"sessionId = {ids[-1]!r}", file=sys.stderr); sys.exit(1)
PY
# stderr anchor line: absolute workspace path + 16-hex project id.
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" >/dev/null \
  || fail "session/new: anchor line missing cwd=$WS"
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" | grep -Eq 'project=[0-9a-f]{16}' \
  || fail "session/new: anchor line project id is not 16-hex"
echo "acp-smoke: session/new ok (anchored to $WS, durable sessionId, project id 16-hex)"

# --- 3. bad cwd fails loud (no silent daemon-dir session) ---------------------
BAD="$SANDBOX/no-such-dir"
rpc 3 session/new "{\"cwd\":\"$BAD\"}"
wait_for '"id":3' 'session/new(bad cwd)' 15
grep '"id":3' "$OUT" | grep -q '"error"'      || fail 'bad cwd: no JSON-RPC error'
grep '"id":3' "$OUT" | grep -q '\-32000'     || fail 'bad cwd: error code != -32000'
echo "acp-smoke: bad cwd rejected loud (-32000)"

# --- 4. stdin EOF → clean shutdown --------------------------------------------
exec 3>&-
wait "$SEEKPID"; rc=$?
[ "$rc" -eq 0 ] || fail "seek acp exited rc=$rc on EOF (want 0)"
echo "acp-smoke: initialize+session/new instance shut down clean (rc=0)"

# --- 5. context-window frames, two states (card c_uzlczr, P0-4) ----------------
# The prompt path is driven for real here, against a loopback fake endpoint —
# the GLM lane is the one whose base URL is env-configurable (GLM_BASE_URL,
# config.BaseURLFor), so the ACP lane can be pointed at it without a flag.
# Two states, because one proves nothing about a threshold:
#
#   low   used ≈ 1012 / 1M  → percent 0, severity ok, NO alert frame
#   full  used ≈ 800012 / 1M → percent 80, severity critical, alert frame
#
# Window + thresholds are asserted as the SAME numbers budget owns
# (glm-5.3 = 1,000,000; 0.60/0.75), which is the whole point of ②: the frame
# is a projection of the TUI/Web meter, not a second table.
command -v python3 >/dev/null 2>&1 || { echo "acp-smoke: FAIL: python3 required for the context-window cases" >&2; exit 1; }
FAKE_PY="$ROOT/eval/tools/fake_paas_glm.py"
[ -f "$FAKE_PY" ] || fail "context-window: fake endpoint missing at $FAKE_PY"

free_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

# ctx_case <label> <prompt_tokens> <completion_tokens> <want_percent> <want_severity> <want_alert 0|1>
ctx_case() {
  local label="$1" prompt="$2" complet="$3" want_pct="$4" want_sev="$5" want_alert="$6"
  local port fifo out err pid i
  port="$(free_port)" || fail "$label: no free port"
  fifo="$SANDBOX/ctx-$label.fifo"; out="$SANDBOX/ctx-$label.ndjson"; err="$SANDBOX/ctx-$label.err"
  mkfifo "$fifo" || fail "$label: mkfifo"

  PORT="$port" PROMPT="$prompt" COMPLET="$complet" python3 "$FAKE_PY" >/dev/null 2>&1 &
  FAKEPID=$!
  i=0
  while ! python3 -c "import socket,sys;s=socket.socket();sys.exit(0 if s.connect_ex(('127.0.0.1',$port))==0 else 1)"; do
    kill -0 "$FAKEPID" 2>/dev/null || fail "$label: fake endpoint died"
    i=$((i + 1)); [ "$i" -ge 50 ] && fail "$label: fake endpoint never listened on :$port"
    sleep 0.2
  done

  env -i PATH="$PATH" HOME="$SMOKE_HOME" SEEK_HOME="$SMOKE_HOME" \
      SEEK_ACP_PROVIDER=glm SEEK_ACP_MODEL=glm-5.3 \
      GLM_API_KEY=selftest-dummy GLM_BASE_URL="http://127.0.0.1:$port/chat/completions" \
      "$SANDBOX/seek" acp < "$fifo" > "$out" 2> "$err" &
  pid=$!
  exec 4> "$fifo"

  echo "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":1}}" >&4
  echo "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"session/new\",\"params\":{\"cwd\":\"$WS\"}}" >&4
  i=0
  while ! grep -q '"id":2' "$out" 2>/dev/null; do
    kill -0 "$pid" 2>/dev/null || { exec 4>&-; kill "$FAKEPID" 2>/dev/null; fail "$label: seek acp died before session/new"; }
    i=$((i + 1)); [ "$i" -ge 75 ] && { exec 4>&-; fail "$label: no session/new within 15s"; }
    sleep 0.2
  done
  local sid
  sid="$(python3 -c 'import json,sys;print([json.loads(l) for l in open(sys.argv[1]) if "\"id\":2" in l][-1]["result"]["sessionId"])' "$out")" \
    || fail "$label: could not read sessionId"
  echo "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"session/prompt\",\"params\":{\"sessionId\":\"$sid\",\"prompt\":[{\"type\":\"text\",\"text\":\"report the context window\"}]}}" >&4
  i=0
  while ! grep -q '"id":3' "$out" 2>/dev/null; do
    kill -0 "$pid" 2>/dev/null || { exec 4>&-; fail "$label: seek acp died during the turn (stderr: $(tail -3 "$err"))"; }
    i=$((i + 1)); [ "$i" -ge 150 ] && { exec 4>&-; fail "$label: turn did not finish within 30s"; }
    sleep 0.2
  done
  exec 4>&-
  wait "$pid"; local rc=$?
  [ "$rc" -eq 0 ] || fail "$label: seek acp exited rc=$rc"
  kill "$FAKEPID" 2>/dev/null
  wait "$FAKEPID" 2>/dev/null
  FAKEPID=""

  # Assert the wire: python3 so the check is on parsed JSON (key presence +
  # values), not on a grep that a reordered object would fool.
  python3 - "$out" "$want_pct" "$want_sev" "$want_alert" <<'PY' || fail "$label: context-window assertions failed"
import json, sys
path, want_pct, want_sev, want_alert = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4] == "1"
frames = []
for line in open(path):
    try:
        msg = json.loads(line)
    except ValueError:
        continue
    if msg.get("method") != "session/update":
        continue
    upd = msg.get("params", {}).get("update", {})
    if upd.get("sessionUpdate") == "usage_update":
        frames.append(upd)
if not frames:
    print("no usage_update frame reached the client", file=sys.stderr); sys.exit(1)
# ACP's own fields, every frame.
for f in frames:
    assert isinstance(f.get("used"), int) and f["used"] > 0, f"bad used: {f}"
    assert isinstance(f.get("size"), int) and f["size"] == 1_000_000, \
        f"size must be budget.Limit(glm-5.3)=1000000, got {f.get('size')}"
ctx = frames[0].get("_meta", {}).get("seek", {}).get("ctx", {})
assert ctx.get("percent") == want_pct, f"percent={ctx.get('percent')} want {want_pct}"
assert ctx.get("severity") == want_sev, f"severity={ctx.get('severity')} want {want_sev}"
alerts = [f for f in frames if f.get("_meta", {}).get("seek", {}).get("ctx", {}).get("alert")]
if want_alert:
    assert len(frames) == 2, f"near-full must add exactly one alert frame, got {len(frames)}"
    assert len(alerts) == 1, f"expected 1 alert frame, got {len(alerts)}"
    if want_sev == "critical":
        assert "/compact" in alerts[0]["_meta"]["seek"]["ctx"].get("notice", ""), \
            f"critical notice must carry the /compact nudge: {alerts[0]}"
else:
    assert not alerts, f"low usage must carry no alert: {alerts}"
print(f"  {path}: {len(frames)} usage_update frame(s), used={frames[0]['used']} size={frames[0]['size']} "
      f"percent={ctx['percent']} severity={ctx['severity']} alert={len(alerts)}")
PY
  echo "acp-smoke: context-window $label ok (percent=$want_pct severity=$want_sev alert=$want_alert)"
}

ctx_case low  1000   12 0  ok       0
ctx_case full 800000 12 80 critical 1

echo "ACP-NDJSON-SMOKE-OK binary=real handshake=initialize+session/new+prompt ctx=low,full anchor=$WS rc=0"
