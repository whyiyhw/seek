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
#             moved (sessionId is the durable transcript id; the stderr
#             anchor line carries the absolute cwd + 16-hex project id +
#             the SAME transcript id the response handed out) → bad cwd
#             fails loud with a JSON-RPC error → [P0-2] reply frames (id,
#             no method) are routed to permission waiters / silently
#             ignored — never answered with -32601 — and $/cancel_request
#             for an unknown id is ignored, both WITHOUT killing the read
#             loop → session/prompt (empty text: terminates without a key
#             or network) asserts the c_vfgeam structured termination —
#             exactly one _seek_turn_stop session/update, stopReason
#             end_turn with a readable detail, preceding the response
#             and matching its spec-side stopReason → [P2] session/new
#             advertises the two-mode table (modes.currentModeId
#             default + availableModes [default, plan]) and
#             session/set_mode is honoured: current_mode_update
#             notification BEFORE the {} response, bogus modeId is a
#             loud -32000 naming the available modes → session/list:
#             initialize must advertise sessionCapabilities.list (object
#             shape {}) and the store scan must return the PRE-SEEDED
#             transcript (id + first-user-message title + header mtime),
#             a no-match cwd filter must answer the empty array → [P1-3]
#             initialize(protocolVersion:2) is answered with the
#             negotiated 1 (+ a stderr downgrade line), an ACP image
#             content block is accepted and degrades silently instead of
#             crashing → stdin EOF → clean exit rc=0.
#   NOT       session/prompt with CONTENT — that needs a real LLM
#             round-trip (key + network), which is not deterministic
#             lane material. The content-bearing termination states
#             (length truncation / cancel / timeout / internal error)
#             are covered in-process by cmd/seek fixture tests
#             (TestACPBackend_Prompt_TerminationFixtures), and the
#             session/request_permission APPROVAL LOOP (P0-2) by
#             cmd/seek's acp_approval_e2e_test.go (real binary × fake
#             OpenAI-compatible backend, four lanes: allow / reject /
#             timeout-deny / yolo-never-asks), and the P1-3 image path
#             (real PNG in, marker into the model's request) by
#             acp_image_e2e_test.go + cmd/seek/testdata/acp-image/*.json
#             (image-only / text+image+text). No API call is made here:
#             a DUMMY DeepSeek key satisfies startup provider resolution
#             (env beats config, DeepSeek wins auto-detect), SEEK_HOME
#             isolates ~/.seek.
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
#
# Pre-seed one transcript (the has-history state for session/list): a
# minimal schema-v3 header line + one user message line is all the
# store's List scan and the title derivation read. The only prompt that
# runs is the empty-text termination probe (no key, no network) on the
# rpc-2 session; the seed pins the deterministic title/mtime assertions.
SEED_ID="20260101-000000-abcdef0123456789"
mkdir -p "$SMOKE_HOME/sessions"
{ printf '{"schema_version":3,"id":"%s","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:01Z","model":"deepseek-v4.1-flash","cwd":"%s","turns":1}\n' "$SEED_ID" "$WS"
  printf '{"role":"user","content":"smoke history question"}\n'
} > "$SMOKE_HOME/sessions/$SEED_ID.jsonl"
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
# sessionCapabilities.list: object shape — `"list":{}` (presence IS the
# advertisement; a boolean here would be schema-invalid).
grep -q '"sessionCapabilities":{"list":{}}' "$OUT" \
  || fail "initialize: sessionCapabilities.list not advertised as {} (got: $(grep '"id":1' "$OUT"))"
echo "acp-smoke: initialize ok (protocolVersion=1, image + sessionCapabilities.list advertised)"

# --- 1b. P1-3: protocolVersion is NEGOTIATED, not echoed ---------------------
# Before card c_4pczu3 the server returned whatever integer the client sent,
# so a client asking for a version seek cannot speak was told "yes" and then
# tripped over wire differences. The spec's rule (and seek's): the response
# carries the NEWEST version the agent supports that is ≤ the request. A
# client asking for 2 must therefore be answered 1 — the downgrade declaration
# is the response itself.
rpc 21 initialize '{"protocolVersion":2}'
wait_for '"id":21' 'initialize(v2 downgrade)' 15
grep '"id":21' "$OUT" | grep -q '"protocolVersion":1' \
  || fail "initialize(2): expected a negotiated downgrade to 1, got $(grep '"id":21' "$OUT")"
grep '"id":21' "$OUT" | grep -q '"image":true' \
  || fail 'initialize(2): downgrade must not drop promptCapabilities.image'
grep -F 'acp: initialize negotiated requested=2 answered=1' "$SMOKE_ERR" >/dev/null \
  || fail 'initialize(2): downgrade not declared on stderr'
echo "acp-smoke: initialize negotiates (requested 2 → answered 1, downgrade declared)"

# --- 2. session/new anchors cwd (D-1 double anchor) ---------------------------
rpc 2 session/new "{\"cwd\":\"$WS\"}"
# trailing comma in the pattern: '"id":2' would substring-match the id:21
# line from the P1-3 negotiate probe above and "pass" before session/new
# has even answered — then the SID extraction below reads an empty string.
wait_for '"id":2,' 'session/new' 15
# sessionId is the DURABLE transcript id (session/load work, 2026-11-19:
# YYYYMMDD-HHMMSS-16hex) — NOT the project name the smoke used to assert
# before persistence landed; that stale assertion sat red until the ACP
# cards reran this smoke — a wire-contract change must update its
# real-binary assertion in the same change. SID is captured because the
# prompt step below addresses the session by it.
SID="$(sed -n 's/.*"sessionId":"\([^"]*\)".*/\1/p' "$OUT" | head -1)"
echo "$SID" | grep -Eq '^[0-9]{8}-[0-9]{6}-[0-9a-f]{16}$' \
  || fail "session/new: sessionId is not a durable transcript id (got: ${SID:-none}; $(grep '"id":2' "$OUT"))"
# stderr anchor line: absolute workspace path + 16-hex project id + the
# SAME transcript id the response handed out (paseo persists that id —
# the anchor line is where the two visibly meet).
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" >/dev/null \
  || fail "session/new: anchor line missing cwd=$WS"
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" | grep -Eq 'project=[0-9a-f]{16}' \
  || fail "session/new: anchor line project id is not 16-hex"
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" | grep -qF "id=$SID" \
  || fail "session/new: anchor line id= does not match the returned sessionId ($SID)"
echo "acp-smoke: session/new ok (anchored to $WS, project id 16-hex, durable id $SID)"

# --- 3. bad cwd fails loud (no silent daemon-dir session) ---------------------
BAD="$SANDBOX/no-such-dir"
rpc 3 session/new "{\"cwd\":\"$BAD\"}"
wait_for '"id":3' 'session/new(bad cwd)' 15
grep '"id":3' "$OUT" | grep -q '"error"'      || fail 'bad cwd: no JSON-RPC error'
grep '"id":3' "$OUT" | grep -q '\-32000'     || fail 'bad cwd: error code != -32000'
echo "acp-smoke: bad cwd rejected loud (-32000)"

# --- 3b. P0-2: reply frames route, never dispatch (approval-lane transport) ---
# session/request_permission made the server RECEIVE reply frames (id,
# no method). Two rules the read loop must now honour, asserted against
# the real binary without an LLM:
#   a) a stray reply nobody waits for is silently ignored — pre-P0-2 it
#      fell into dispatch's default case and got ANSWERED with a -32601
#      error, protocol noise on every permission answer;
#   b) $/cancel_request for an unknown id is ignored.
# Both must leave the read loop alive (the initialize probe after proves
# it) and neither may ever produce a "method not handled" error.
printf '%s\n' '{"jsonrpc":"2.0","id":"p99","result":{"outcome":{"outcome":"cancelled"}}}' >&3
printf '%s\n' '{"jsonrpc":"2.0","method":"$/cancel_request","params":{"id":"p1"}}' >&3
rpc 4 initialize '{"protocolVersion":1}'
wait_for '"id":4' 'post-stray-frames initialize' 15
if grep -q 'method not handled' "$OUT"; then
  fail 'stray reply/cancel frames were dispatched as methods (-32601 noise)'
fi
echo "acp-smoke: reply + cancel frames routed/ignored, read loop alive (P0-2)"

# --- 3c. session/prompt termination frame (c_vfgeam) ---------------------------
# The one prompt shape that terminates deterministically WITHOUT a key
# or network: an empty text block routes to no text at all, so the
# backend answers immediately. That is enough to assert the structured
# termination on the REAL binary: exactly one _seek_turn_stop
# session/update, carrying the frame stopReason, AND it precedes the
# session/prompt response. (The other termination states — length
# truncation, cancel, timeout, internal error — need an LLM round-trip
# or a stalled upstream; they are covered by the cmd/seek fixture
# tests, TestACPBackend_Prompt_TerminationFixtures.)
rpc 5 session/prompt "{\"sessionId\":\"$SID\",\"prompt\":[{\"type\":\"text\",\"text\":\"\"}]}"
wait_for '_seek_turn_stop' 'session/prompt termination frame' 15
wait_for '"id":5' 'session/prompt response' 15
# exactly one termination frame per prompt…
frames=$(grep -c -F '"_seek_turn_stop"' "$OUT")
[ "$frames" -eq 1 ] || fail "termination frames = $frames, want exactly 1 per session/prompt"
# …it says end_turn with a readable detail…
grep -F '"sessionUpdate":"_seek_turn_stop"' "$OUT" | grep -qF '"stopReason":"end_turn"' \
  || fail "termination frame stopReason != end_turn: $(grep -F '_seek_turn_stop' "$OUT")"
grep -F '"_seek_turn_stop"' "$OUT" | grep -qF '"detail":' \
  || fail "termination frame carries no readable detail"
# …and it PRECEDES the response (frame-then-response is the ordering
# clients rely on to attribute a missing frame to process death).
frame_line=$(grep -n -F '"_seek_turn_stop"' "$OUT" | head -1 | cut -d: -f1)
resp_line=$(grep -n -F '"id":5' "$OUT" | head -1 | cut -d: -f1)
[ "$frame_line" -lt "$resp_line" ] || fail "termination frame (line $frame_line) must precede the response (line $resp_line)"
# the response keeps the spec-side stopReason in sync with the frame.
grep -F '"id":5' "$OUT" | grep -qF '"stopReason":"end_turn"' \
  || fail "response stopReason != end_turn: $(grep -F '"id":5' "$OUT")"
echo "acp-smoke: termination frame ok (_seek_turn_stop end_turn, precedes response)"

# --- 3d. session modes: advertisement + set_mode (c_cotikz, ACP·P2) -------------
# The plan-mode protocol channel, asserted on the real binary without
# an LLM: session/new advertised the two-mode table (currentModeId
# default + available [default, plan]); set_mode plan is honoured with
# a current_mode_update notification BEFORE the {} response (the
# response itself carries no mode info); an unknown modeId is a loud
# -32000 naming the available modes; switching back confirms default.
grep '"id":2' "$OUT" | grep -q '"modes":{"currentModeId":"default"' \
  || fail "session/new: no modes advertisement: $(grep '"id":2' "$OUT")"
grep '"id":2' "$OUT" | grep -q '"availableModes":\[{"id":"default","name":"Default"' \
  || fail "session/new: availableModes missing default entry"
grep '"id":2' "$OUT" | grep -q '"id":"plan","name":"Plan"' \
  || fail "session/new: availableModes missing plan entry"
rpc 6 session/set_mode "{\"sessionId\":\"$SID\",\"modeId\":\"plan\"}"
wait_for 'current_mode_update' 'set_mode(plan) notification' 15
grep -F '"current_mode_update"' "$OUT" | grep -qF '"currentModeId":"plan"' \
  || fail "current_mode_update does not name plan"
# notification precedes the response (same ordering contract as
# _seek_turn_stop above).
mode_notif_line=$(grep -n -F '"current_mode_update"' "$OUT" | head -1 | cut -d: -f1)
mode_resp_line=$(grep -n -F '"id":6' "$OUT" | head -1 | cut -d: -f1)
[ "$mode_notif_line" -lt "$mode_resp_line" ] \
  || fail "current_mode_update (line $mode_notif_line) must precede the set_mode response (line $mode_resp_line)"
grep -F '"id":6' "$OUT" | grep -qF '"result":{}' \
  || fail "set_mode result is not {} (schema response is _meta-only): $(grep -F '"id":6' "$OUT")"
rpc 7 session/set_mode "{\"sessionId\":\"$SID\",\"modeId\":\"bogus\"}"
wait_for '"id":7' 'set_mode(bogus) response' 15
grep -F '"id":7' "$OUT" | grep -q '\-32000' \
  || fail "bogus modeId not rejected with -32000"
grep -F '"id":7' "$OUT" | grep -q 'available' \
  || fail "bogus modeId error does not name the available modes"
rpc 8 session/set_mode "{\"sessionId\":\"$SID\",\"modeId\":\"default\"}"
wait_for '"id":8' 'set_mode(default) response' 15
grep -F '"current_mode_update"' "$OUT" | grep -qF '"currentModeId":"default"' \
  || fail "current_mode_update does not confirm default"
echo "acp-smoke: session modes ok (advertised default+plan, set_mode honoured + notified, bogus rejected)"

# --- 3e. session/list: the pre-seeded transcript, both store states -----------
# Has history: the seed comes back with id + first-user-message title
# + header mtime. (The rpc-2 session may share the cwd filter and also
# appear — these are contains-assertions on the seed, extras are fine.)
rpc 9 session/list "{\"cwd\":\"$WS\"}"
wait_for '"id":9' 'session/list' 15
grep '"id":9' "$OUT" | grep -q "\"sessionId\":\"$SEED_ID\"" \
  || fail "session/list: seeded id missing (got: $(grep '"id":9' "$OUT"))"
grep '"id":9' "$OUT" | grep -q '"title":"smoke history question"' \
  || fail "session/list: title not derived from the first user message (got: $(grep '"id":9' "$OUT"))"
grep '"id":9' "$OUT" | grep -q '"updatedAt":"2026-01-01T00:00:01Z"' \
  || fail "session/list: updatedAt not the header mtime (got: $(grep '"id":9' "$OUT"))"
# Empty result state: a cwd nothing matches answers the EMPTY ARRAY —
# the schema requires the array member (never null).
rpc 10 session/list "{\"cwd\":\"$SANDBOX/no-such-cwd\"}"
wait_for '"id":10' 'session/list(no match)' 15
grep '"id":10' "$OUT" | grep -q '"sessions":\[\]' \
  || fail "session/list: no-match filter must answer sessions:[] (got: $(grep '"id":10' "$OUT"))"
echo "acp-smoke: session/list ok (seeded transcript listed + title + mtime; empty filter = [])"

# --- 3f. P1-3: an image content block is accepted, never fatal ----------------
# ACP image blocks (type:"image" + base64 data + mimeType) must be decoded
# and handled, not crash the server. This probe is deterministic WITHOUT an
# LLM: the block's base64 is corrupt, so the block degrades to nothing, the
# prompt text stays empty, and acpBackend.Prompt answers end_turn immediately
# (the same short-circuit a pure image prompt used to hit — now reached only
# after the block has actually been parsed). A crash or a -32000 here means
# the image block shape regressed. It runs AFTER the termination probe on
# purpose: every prompt now draws a _seek_turn_stop frame (c_vfgeam), so
# this second prompt would break the "exactly one frame" count above.
# (probe id 11: 6-8 are taken by set_mode, 9-10 by session/list — renumber
# before appending further probes, see the smoke-probe-ids pitfall.)
# The full path — real PNG in, marker into the model's request — is the
# fixture e2e: cmd/seek/testdata/acp-image/{image-only,mixed}.json driven by
# cmd/seek/acp_image_e2e_test.go against the real binary + a fake backend.
rpc 11 session/prompt '{"sessionId":"smoke","prompt":[{"type":"image","data":"!!!not-base64!!!","mimeType":"image/png"}]}'
wait_for '"id":11,' 'session/prompt(image block)' 20
grep '"id":11' "$OUT" | grep -q '"stopReason":"end_turn"' \
  || fail "image block: expected end_turn, got $(grep '"id":11' "$OUT")"
grep '"id":11' "$OUT" | grep -q '"error"' \
  && fail 'image block: corrupt block must degrade silently, not error'
kill -0 "$SEEKPID" 2>/dev/null || fail 'image block: server died on an image content block'
if grep -qi 'panic' "$SMOKE_ERR"; then
  fail 'image block: panic in stderr'
fi
echo "acp-smoke: image content block accepted + degraded silently, read loop alive (P1-3)"

# --- 4. stdin EOF → clean shutdown --------------------------------------------
exec 3>&-
wait "$SEEKPID"; rc=$?
[ "$rc" -eq 0 ] || fail "seek acp exited rc=$rc on EOF (want 0)"
echo "ACP-NDJSON-SMOKE-OK binary=real handshake=initialize+session/new+reply-routing+prompt-term+modes+session-list+image-block anchor=$WS rc=0"
