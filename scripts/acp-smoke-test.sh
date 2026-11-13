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
# moves it behind the registered selftest entry (`bash scripts/selftest.sh
# acp-smoke`), which the lane whitelist does approve.
#
# WHAT IT COVERS (and what it deliberately does NOT)
#
#   covered   build → spawn real `seek acp` → newline-JSON-RPC handshake
#             (initialize) → session/new with a cwd that differs from the
#             process launch dir → assert the D-1 double anchor actually
#             moved (sessionId = basename, stderr anchor line carries the
#             absolute cwd + 16-hex project id) → bad cwd fails loud with
#             a JSON-RPC error → stdin EOF → clean exit rc=0.
#   NOT       session/prompt — that needs a real LLM round-trip (key +
#             network), which is not deterministic lane material. The
#             prompt path is covered in-process by internal/acp tests
#             with fake backends, and by manual real-key smoke when
#             needed. No API call is made here: a DUMMY DeepSeek key
#             satisfies startup provider resolution (env beats config,
#             DeepSeek wins auto-detect), SEEK_HOME isolates ~/.seek.
#
# Marker on success: ACP-NDJSON-SMOKE-OK (echoes the D1-SMOKE-OK lineage
# from c_bnjrw5's hand-run smoke).
#
# Usage: scripts/acp-smoke-test.sh   (exit 0 = all green, 1 = failure)
# Env:   none required; everything is built under a mktemp sandbox that
#        is removed on exit. Bash 3.2 compatible (no coproc, no mapfile).

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
trap ' [ -n "${SEEKPID:-}" ] && kill "${SEEKPID}" 2>/dev/null; rm -rf "$SANDBOX" ' EXIT
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
grep -q "\"sessionId\":\"smoke-ws\"" "$OUT" || fail "session/new: sessionId != smoke-ws (got: $(grep '"id":2' "$OUT"))"
# stderr anchor line: absolute workspace path + 16-hex project id.
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" >/dev/null \
  || fail "session/new: anchor line missing cwd=$WS"
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" | grep -Eq 'project=[0-9a-f]{16}' \
  || fail "session/new: anchor line project id is not 16-hex"
echo "acp-smoke: session/new ok (anchored to $WS, project id 16-hex)"

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
echo "ACP-NDJSON-SMOKE-OK binary=real handshake=initialize+session/new anchor=$WS rc=0"
