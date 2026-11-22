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
#             the SAME transcript id the response handed out) → [backlog ⑦,
#             c_oyt6ft] the MCP passthrough TWO-STATE fixture: session/new
#             WITHOUT mcpServers is a byte-identical no-op (no `acp: mcp:`
#             stderr line at all), session/new WITH mcpServers declaring a
#             real stdio server (a shell stub) is connected via the same
#             client stack mcp.json uses, its tools registered as
#             `mcp__<server>__<tool>`, the child pid proven live — and
#             stdin EOF reaps it (pid probe, the shell-side leaktest) →
#             bad cwd fails loud with a JSON-RPC error → [P0-2] reply
#             frames (id, no method) are routed to permission waiters /
#             silently ignored — never answered with -32601 — and
#             $/cancel_request for an unknown id is ignored, both WITHOUT
#             killing the read loop → session/prompt (empty text:
#             terminates without a key or network) asserts the c_vfgeam
#             structured termination — exactly one _seek_turn_stop
#             session/update, stopReason end_turn with a readable detail,
#             preceding the response and matching its spec-side
#             stopReason → [P2] session/new advertises the two-mode table
#             (modes.currentModeId default + availableModes [default,
#             plan]) and session/set_mode is honoured:
#             current_mode_update notification BEFORE the {} response,
#             bogus modeId is a loud -32000 naming the available modes →
#             session/list: initialize must advertise
#             sessionCapabilities.list (object shape {}) and the store
#             scan must return the PRE-SEEDED transcript (id +
#             first-user-message title + header mtime), a no-match cwd
#             filter must answer the empty array → [P1-3]
#             initialize(protocolVersion:2) is answered with the
#             negotiated 1 (+ a stderr downgrade line), an ACP image
#             content block is accepted and degrades silently instead of
#             crashing → [P1-2] a SECOND session/new mints a NEW durable
#             session id (one process, N conversations), its anchor line
#             lands on stderr, and its first prompt activates it (the
#             stderr switch line) and terminates with a frame tagged by
#             ITS sessionId → stdin EOF → clean exit rc=0 → [P0-4, c_uzlczr]
#             context-window frames in TWO states — low usage and
#             near-full — each a fresh `seek acp` process driving one
#             real session/prompt turn against a loopback fake GLM
#             endpoint (eval/tools/fake_paas_glm.py, stdlib python3;
#             reused, no new fixture): asserts one `usage_update`
#             session/update per turn with ACP's own used/size fields,
#             seek's percent/severity under _meta.seek.ctx, and —
#             near-full only — the threshold alert frame whose notice
#             carries the /compact nudge. size is cross-checked against
#             budget.Limit(glm-5.3) = 1M, so the assertion is on the
#             same-source reading the TUI/Web meters use, not on a copy.
#   NOT       session/prompt against a REAL LLM — every prompt here is
#             either the empty-text short-circuit (no key, no network)
#             or answered by the loopback fake, so nothing can cost
#             money or flake on a model. The content-bearing termination
#             states
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
# from c_bnjrw5's hand-run smoke) + ACP-MCP-SMOKE-OK.
#
# Usage: scripts/acp-smoke-test.sh   (exit 0 = all green, 1 = failure)
# Env:   none required; everything is built under a mktemp sandbox that
#        is removed on exit. Bash 3.2 compatible (no coproc, no mapfile).
#        Needs go + python3 (the loopback fake GLM endpoint + the parsed-
#        JSON assertions; precedent: transcript-stats-test pins python3).

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
trap ' [ -n "${SEEKPID:-}" ] && kill "${SEEKPID}" 2>/dev/null; [ -n "${FAKEPID:-}" ] && kill "${FAKEPID}" 2>/dev/null; rm -rf "$SANDBOX" ' EXIT
SMOKE_HOME="$SANDBOX/home"; mkdir -p "$SMOKE_HOME"
LAUNCH="$SANDBOX/launch";    mkdir -p "$LAUNCH"
WS="$SANDBOX/smoke-ws";      mkdir -p "$WS"
FIFO="$SANDBOX/in.fifo"; mkfifo "$FIFO"
OUT="$SANDBOX/acp-out.ndjson"
SMOKE_ERR="$SANDBOX/acp-stderr.log"
SEEKPID=""
FAKEPID=""

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

# --- 2b. state A: NO mcpServers ⇒ zero passthrough -----------------------------
# The card's "不带 mcpServers 行为零变化" half. Asserted from the outside:
# the only stderr line the passthrough can emit is `acp: mcp: …`, so its
# absence IS the no-op — no child spawned, no tool registered, no log
# line the pre-passthrough binary would not have produced.
if grep -q '^acp: mcp' "$SMOKE_ERR"; then
  fail "session/new without mcpServers must not touch the MCP lane (got: $(grep '^acp: mcp' "$SMOKE_ERR"))"
fi
echo "acp-smoke: session/new without mcpServers is a no-op (no acp: mcp line)"

# --- 2c. state B: mcpServers declaring a stdio server is connected --------------
# A real stdio MCP server, written into the sandbox: POSIX sh, no jq, no
# python. It records its pid (proving seek actually spawned it) and answers
# initialize / tools/list / tools/call. One tool is deliberately dotted —
# MCP allows `card.read`, the model APIs do not, so this also proves the
# name folding.
STUB="$SANDBOX/mcp-stub.sh"
MCPPID="$SANDBOX/mcp-stub.pid"
cat > "$STUB" <<'STUB_EOF'
#!/bin/sh
printf '%s' "$$" > "$ACP_SMOKE_MCP_PIDFILE"
while IFS= read -r line; do
  case "$line" in
    *'"id"'*) ;;                 # request
    *) continue ;;               # notification (notifications/initialized)
  esac
  id=$(printf '%s' "$line" | sed 's/.*"id":\([0-9][0-9]*\).*/\1/')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"acp-smoke-stub","version":"0"}}}\n' "$id"
      ;;
    *'"method":"tools/list"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo","description":"echo the card id","inputSchema":{"type":"object","properties":{"card":{"type":"string"}}}},{"name":"card.read","description":"dotted tool name","inputSchema":{"type":"object"}}]}}\n' "$id"
      ;;
    *'"method":"tools/call"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"stub"}]}}\n' "$id"
      ;;
    *)
      printf '{"jsonrpc":"2.0","id":%s,"result":{}}\n' "$id"
      ;;
  esac
done
STUB_EOF
chmod +x "$STUB" || fail "chmod stub"

# Probe id 14 (1-13 and 21 are taken by the probes below and further
# down — 12/13 now belong to the P1-2 two-session probes; renumber from
# the next free id before appending, see the smoke-probe-ids pitfall).
# Trailing comma in wait_for: '"id":14' keeps the match prefix-free
# against future 14x ids.
# This MUST run before the first session/prompt (probe 5): a turn freezes
# the tools array (tools.Registry.Wire), and a roster arriving after the
# freeze is refused by design — state B would silently become state A.
# Under P1-2 multi-session this session/new MINTS the second session
# (record-only anchor) — its stderr anchor line is why 3g below counts 3.
rpc 14 session/new "{\"cwd\":\"$WS\",\"mcpServers\":[{\"name\":\"butler\",\"command\":\"$STUB\",\"args\":[],\"env\":[{\"name\":\"ACP_SMOKE_MCP_PIDFILE\",\"value\":\"$MCPPID\"}]}]}"
wait_for '"id":14,' 'session/new(mcpServers)' 20
grep '"id":14' "$OUT" | grep -q '"sessionId"' \
  || fail "session/new with mcpServers must still answer: $(grep '"id":14' "$OUT")"

# The declaration was connected and its tools landed under the three-segment
# prefix. Both assertions are on seek's own log line, i.e. on the same path
# the model's tools array takes.
grep -q 'acp: mcp: registered 2 tool(s)' "$SMOKE_ERR" \
  || fail "mcpServers: no registration line (got: $(grep '^acp: mcp' "$SMOKE_ERR"))"
grep -q 'mcp__butler__echo' "$SMOKE_ERR" \
  || fail "mcpServers: tool not registered as mcp__butler__echo (got: $(grep '^acp: mcp' "$SMOKE_ERR"))"
grep -q 'mcp__butler__card_read' "$SMOKE_ERR" \
  || fail "mcpServers: dotted tool name was not folded to mcp__butler__card_read"

# The child really exists (spawned with the declared env, which is the only
# way an injected server gets any credential — seek scrubs the parent env).
[ -f "$MCPPID" ] || fail "mcpServers: declared server never started"
CHILD=$(cat "$MCPPID")
kill -0 "$CHILD" 2>/dev/null || fail "mcpServers: child pid $CHILD is not alive"
echo "acp-smoke: session/new with mcpServers ok (child pid $CHILD, tools mcp__butler__*)"

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

# --- 3g. P1-2: a second session/new mints a NEW session (backlog ③) ----
# One seek process now serves N conversations: the second session/new
# must return a DIFFERENT durable transcript id (not re-anchor the live
# one), the anchor line lands on stderr for it too, and its first prompt
# activates it (the "session switched" stderr line) and answers with a
# termination frame tagged with ITS sessionId. No key / no network: the
# empty-text prompt is the same deterministic termination shape probe 5
# used. (probe ids 12/13 — the MCP passthrough probe took 14 upstream;
# see the smoke-probe-ids pitfall.)
rpc 12 session/new "{\"cwd\":\"$WS\"}"
wait_for '"id":12,' 'session/new(second)' 15
SID2="$(grep '"id":12,' "$OUT" | sed -n 's/.*"sessionId":"\([^"]*\)".*/\1/p' | head -1)"
echo "$SID2" | grep -Eq '^[0-9]{8}-[0-9]{6}-[0-9a-f]{16}$' \
  || fail "second session/new: sessionId is not a durable id (got: ${SID2:-none}; $(grep '"id":12,' "$OUT"))"
[ "$SID2" != "$SID" ] \
  || fail "second session/new returned the SAME id $SID — must mint a new session"
anchored_lines=$(grep -c -F 'acp: session anchored' "$SMOKE_ERR")
[ "$anchored_lines" -eq 3 ] \
  || fail "anchor lines = $anchored_lines, want 3 (rpc 2 + the MCP-passthrough session + this one)"
grep -F "acp: session anchored cwd=$WS" "$SMOKE_ERR" | grep -qF "id=$SID2" \
  || fail "second session's anchor line missing id=$SID2"
echo "acp-smoke: second session/new ok (new durable id $SID2 ≠ $SID, anchor line present)"

# Its first prompt activates the session (per-session cwd/state install)
# and terminates cleanly — the frame is tagged with THIS session's id.
rpc 13 session/prompt "{\"sessionId\":\"$SID2\",\"prompt\":[{\"type\":\"text\",\"text\":\"\"}]}"
wait_for '"id":13,' 'session/prompt(second session)' 15
grep -F '_seek_turn_stop' "$OUT" | grep -qF "\"sessionId\":\"$SID2\"" \
  || fail "second session's termination frame not tagged with its id"
grep -F '"id":13' "$OUT" | grep -qF '"stopReason":"end_turn"' \
  || fail "second session's prompt response != end_turn: $(grep '"id":13' "$OUT")"
grep -qF "acp: session switched id=$SID2" "$SMOKE_ERR" \
  || fail "second session's first prompt did not activate it (no switch line)"
kill -0 "$SEEKPID" 2>/dev/null || fail 'second session: server died'
echo "acp-smoke: two-session process ok (activation + per-session termination frame, read loop alive)"

# --- 4. stdin EOF → clean shutdown --------------------------------------------
exec 3>&-
wait "$SEEKPID"; rc=$?
[ "$rc" -eq 0 ] || fail "seek acp exited rc=$rc on EOF (want 0)"

# The leaktest half of card c_oyt6ft: session end must reap the
# client-injected child. seek's teardown kills the exec context and
# Waits, so the pid is gone when the process returns; poll briefly for
# the kernel to finish.
left=0
while [ "$left" -lt 50 ]; do
  kill -0 "$CHILD" 2>/dev/null || break
  sleep 0.1; left=$((left + 1))
done
kill -0 "$CHILD" 2>/dev/null && fail "MCP child $CHILD survived session end (leak)"
grep -q 'acp: mcp closed 1 server(s)' "$SMOKE_ERR" \
  || fail "session end: teardown line missing (got: $(grep '^acp: mcp' "$SMOKE_ERR"))"
echo "acp-smoke: session end reaped the MCP child (pid $CHILD gone)"
echo "acp-smoke: initialize+session/new instance shut down clean (rc=0)"

# --- 5. context-window frames, two states (P0-4, c_uzlczr) ---------------------
# Each state is a FRESH `seek acp` process driving one real session/prompt
# turn against a loopback fake GLM endpoint — the GLM lane is the one whose
# base URL is env-configurable (GLM_BASE_URL → config.BaseURLFor), so the
# ACP lane can be pointed at it without a flag. Two states, because one
# proves nothing about a threshold:
#
#   low   used = 1012 / 1M  → percent 0,  severity ok,       NO alert frame
#   full  used = 800012 / 1M → percent 80, severity critical, alert frame
#
# Window + thresholds are asserted as the SAME numbers budget owns
# (glm-5.3 = 1,000,000; 0.60/0.75) — the whole point of acceptance ②: the
# frame is a projection of the TUI/Web meter, not a second table.
# Probe ids 12-14 per case (fresh process, fresh output file — the id
# space of the main instance above never meets these): 1-10, 21 and 11
# are taken on main, new probes start at 12 (smoke-probe-ids pitfall).
command -v python3 >/dev/null 2>&1 || { echo "acp-smoke: FAIL: python3 required for the context-window cases" >&2; exit 1; }
FAKE_PY="$ROOT/eval/tools/fake_paas_glm.py"
[ -f "$FAKE_PY" ] || fail "context-window: fake endpoint missing at $FAKE_PY"

free_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
}

# ctx_case <label> <prompt_tokens> <completion_tokens> <want_percent> <want_severity> <want_alert 0|1>
ctx_case() {
  local label="$1" prompt="$2" complet="$3" want_pct="$4" want_sev="$5" want_alert="$6"
  local port fifo out err pid i sid rc
  port="$(free_port)" || fail "$label: no free port"
  fifo="$SANDBOX/ctx-$label.fifo"; out="$SANDBOX/ctx-$label.ndjson"; err="$SANDBOX/ctx-$label.err"
  mkfifo "$fifo" || fail "$label: mkfifo"

  # The fake GLM endpoint answers one SSE stream whose usage chunk carries
  # exactly PROMPT/COMPLET tokens — env-steered, stdlib only.
  PORT="$port" PROMPT="$prompt" COMPLET="$complet" CACHED=0 python3 "$FAKE_PY" >/dev/null 2>&1 &
  FAKEPID=$!
  i=0
  while ! python3 -c "import socket,sys;s=socket.socket();sys.exit(0 if s.connect_ex(('127.0.0.1',$port))==0 else 1)"; do
    kill -0 "$FAKEPID" 2>/dev/null || fail "$label: fake endpoint died"
    i=$((i + 1)); [ "$i" -ge 50 ] && fail "$label: fake endpoint never listened on :$port"
    sleep 0.2
  done

  # env -i: the ctx instance must not inherit the caller's provider keys or
  # base URLs — the GLM lane here is pinned entirely by these five vars.
  env -i PATH="$PATH" HOME="$SMOKE_HOME" SEEK_HOME="$SMOKE_HOME" \
      SEEK_ACP_PROVIDER=glm SEEK_ACP_MODEL=glm-5.3 \
      GLM_API_KEY=selftest-dummy GLM_BASE_URL="http://127.0.0.1:$port/chat/completions" \
      "$SANDBOX/seek" acp < "$fifo" > "$out" 2> "$err" &
  pid=$!
  exec 4> "$fifo"

  echo '{"jsonrpc":"2.0","id":12,"method":"initialize","params":{"protocolVersion":1}}' >&4
  echo "{\"jsonrpc\":\"2.0\",\"id\":13,\"method\":\"session/new\",\"params\":{\"cwd\":\"$WS\"}}" >&4
  i=0
  while ! grep -q '"id":13,' "$out" 2>/dev/null; do
    kill -0 "$pid" 2>/dev/null || { exec 4>&-; kill "$FAKEPID" 2>/dev/null; fail "$label: seek acp died before session/new"; }
    i=$((i + 1)); [ "$i" -ge 75 ] && { exec 4>&-; fail "$label: no session/new within 15s"; }
    sleep 0.2
  done
  sid="$(python3 -c 'import json,sys;print([json.loads(l) for l in open(sys.argv[1]) if "\"id\":13" in l][-1]["result"]["sessionId"])' "$out")" \
    || fail "$label: could not read sessionId"
  echo "{\"jsonrpc\":\"2.0\",\"id\":14,\"method\":\"session/prompt\",\"params\":{\"sessionId\":\"$sid\",\"prompt\":[{\"type\":\"text\",\"text\":\"report the context window\"}]}}" >&4
  i=0
  while ! grep -q '"id":14,' "$out" 2>/dev/null; do
    kill -0 "$pid" 2>/dev/null || { exec 4>&-; kill "$FAKEPID" 2>/dev/null; fail "$label: seek acp died during the turn (stderr: $(tail -3 "$err" 2>/dev/null))"; }
    i=$((i + 1)); [ "$i" -ge 150 ] && { exec 4>&-; fail "$label: turn did not finish within 30s"; }
    sleep 0.2
  done
  exec 4>&-
  wait "$pid"; rc=$?
  [ "$rc" -eq 0 ] || fail "$label: seek acp exited rc=$rc"
  kill "$FAKEPID" 2>/dev/null
  wait "$FAKEPID" 2>/dev/null
  FAKEPID=""

  # Assert the wire: python3, so the check is on parsed JSON (key presence
  # + values), not on a grep that a reordered object would fool.
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
    assert not alerts, f"low usage must carry no alert: {frames}"
print(f"  {path}: {len(frames)} usage_update frame(s), used={frames[0]['used']} size={frames[0]['size']} "
      f"percent={ctx['percent']} severity={ctx['severity']} alert={len(alerts)}")
PY
  echo "acp-smoke: context-window $label ok (percent=$want_pct severity=$want_sev alert=$want_alert)"
}

ctx_case low   1000 12 0  ok       0
ctx_case full 800000 12 80 critical 1

echo "ACP-NDJSON-SMOKE-OK binary=real handshake=initialize+session/new+reply-routing+prompt-term+modes+session-list+image-block+two-session ctx=low,full anchor=$WS rc=0"
echo "ACP-MCP-SMOKE-OK two-state=no-roster/noop+roster/connected tools=mcp__butler__* child=$CHILD reaped"
