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
#             moved (sessionId = basename, stderr anchor line carries the
#             absolute cwd + 16-hex project id) → bad cwd fails loud with
#             a JSON-RPC error → stdin EOF → clean exit rc=0.
#   covered   MCP passthrough (backlog ⑦, card c_oyt6ft), the TWO-STATE
#             fixture the card asks for:
#               state A — session/new WITHOUT mcpServers: byte-identical
#                 behaviour, asserted as "no child process, no tool
#                 registered" (stderr carries no `acp: mcp:` line at all);
#               state B — session/new WITH mcpServers declaring a real
#                 stdio server (a shell stub that speaks MCP over stdio):
#                 seek spawns it, lists its tools, and registers them as
#                 `mcp__<server>__<tool>`; then stdin EOF → the child is
#                 reaped (pid probe, the shell-side leaktest).
#   NOT       session/prompt — that needs a real LLM round-trip (key +
#             network), which is not deterministic lane material. The
#             prompt path is covered in-process by internal/acp tests
#             with fake backends, and by manual real-key smoke when
#             needed. No API call is made here: a DUMMY DeepSeek key
#             satisfies startup provider resolution (env beats config,
#             DeepSeek wins auto-detect), SEEK_HOME isolates ~/.seek.
#
# Marker on success: ACP-NDJSON-SMOKE-OK (echoes the D1-SMOKE-OK lineage
# from c_bnjrw5's hand-run smoke) + ACP-MCP-SMOKE-OK.
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
grep '"id":2' "$OUT" | grep -Eq '"sessionId":"([0-9]{8}-[0-9]{6}-[0-9a-f]{16}|smoke-ws)"' \
  || fail "session/new: sessionId is neither the durable transcript id nor the project name (got: $(grep '"id":2' "$OUT"))"
# stderr anchor line: absolute workspace path + 16-hex project id.
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" >/dev/null \
  || fail "session/new: anchor line missing cwd=$WS"
grep -F "acp: session anchored cwd=$WS project=" "$SMOKE_ERR" | grep -Eq 'project=[0-9a-f]{16}' \
  || fail "session/new: anchor line project id is not 16-hex"
echo "acp-smoke: session/new ok (anchored to $WS, project id 16-hex)"

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

rpc 4 session/new "{\"cwd\":\"$WS\",\"mcpServers\":[{\"name\":\"butler\",\"command\":\"$STUB\",\"args\":[],\"env\":[{\"name\":\"ACP_SMOKE_MCP_PIDFILE\",\"value\":\"$MCPPID\"}]}]}"
wait_for '"id":4' 'session/new(mcpServers)' 20
grep '"id":4' "$OUT" | grep -q '"sessionId"' \
  || fail "session/new with mcpServers must still answer: $(grep '"id":4' "$OUT")"

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

# --- 4. stdin EOF → clean shutdown + MCP children reaped ----------------------
exec 3>&-
wait "$SEEKPID"; rc=$?
[ "$rc" -eq 0 ] || fail "seek acp exited rc=$rc on EOF (want 0)"

# The leaktest half of the card: session end must reap the client-injected
# child. seek's teardown kills the exec context and Waits, so the pid is
# gone when the process returns; poll briefly for the kernel to finish.
left=0
while [ "$left" -lt 50 ]; do
  kill -0 "$CHILD" 2>/dev/null || break
  sleep 0.1; left=$((left + 1))
done
kill -0 "$CHILD" 2>/dev/null && fail "MCP child $CHILD survived session end (leak)"
grep -q 'acp: mcp closed 1 server(s)' "$SMOKE_ERR" \
  || fail "session end: teardown line missing (got: $(grep '^acp: mcp' "$SMOKE_ERR"))"
echo "acp-smoke: session end reaped the MCP child (pid $CHILD gone)"

echo "ACP-NDJSON-SMOKE-OK binary=real handshake=initialize+session/new anchor=$WS rc=0"
echo "ACP-MCP-SMOKE-OK two-state=no-roster/noop+roster/connected tools=mcp__butler__* child=$CHILD reaped"
