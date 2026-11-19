#!/bin/bash
# eval-provider-test.sh — eval/run.sh provider 车道（c_ffzic2，2026-11-19）
# 的确定性自测。脚本自动进 selftest 发现名单，裸跑即自测。
#
# 为什么存在：批量/无人值守 eval 不得烧计量 DeepSeek key（v8.md §9.1
# 口径：测试默认 OpenCode Go 套餐 DeepSeek，订阅内）。本脚本把「车道真的
# 切过去了」的证明收进受审代码，全部在假端点/假 key 下进行：
#
#   ① 配置读取：SEEK_EVAL_PROVIDER/MODEL/BASE_URL/API_KEY 四旋钮 →
#      默认调用的 argv（-provider/-model/-base-url）与导出的车道环境
#      变量（OPENCODE_API_KEY / OPENAI_API_KEY / OPENCODE_BASE_URL /
#      SEEK_EVAL_PROVIDER_ARGS）。假二进制记录 argv+env 视图逐项断言；
#      四旋钮全不设 = 历史行为（argv 无 -provider，计量 key 原样可见）。
#   ② 请求目标切换 + 计量 key 零引用：真 seek 二进制 × 本地假
#      OpenAI 兼容端点（eval/tools/fake_openai_compat.py 记录每个 POST
#      的 path/auth/model）。compatible 与 opencode 两条车道各跑一遍：
#      断言请求打到了假端点的路径、Bearer 是假车道 key、body 里的 model
#      与旋钮一致（opencode 不传 -model 时线上就是 deepseek-v4.1-flash
#      ——车道缺省模型的在线证明）；计量 key 金丝雀（env 侧 + 临时
#      SEEK_HOME 的 config 侧各埋一只）在服务端日志零出现；
#      opencode 车道还埋了 config preset 劫持端点，断言被
#      OPENCODE_BASE_URL 导出压过（请求没打到劫持端口）。
#   ③ runner 转发契约：带 runner.sh 的用例收到导出的
#      SEEK_EVAL_PROVIDER_ARGS / 车道凭证 env、且看不到被擦除的
#      DEEPSEEK_API_KEY（mcp-arm-runner 类自建调用转发的一行接线形状）。
#   ④ 形状校验：compatible 缺 BASE_URL / 未知 provider → exit 2 带说明。
#
# 全部产物落 mktemp 沙盒与 eval/cases/zprovider-*（z 前缀远离真实用例
# 域），trap 退场清干净；eval/results/ 全程零接触（所有 run.sh 调用都
# 重定向 SEEK_EVAL_RESULTS_DIR）。
#
# macOS bash 3.2 兼容（selftest 通道全仓口径）：无 mapfile / 关联数组。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-provider selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-provider selftest: jq required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "eval-provider selftest: python3 required" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "eval-provider selftest: curl required" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "eval-provider selftest: go required (builds the real binary)" >&2; exit 2; }

CASE1=zprovider-selftest
CASE2=zprovider-runner-selftest
tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-provider-selftest.XXXXXX")
tmp=$(cd "$tmp" && pwd) || exit 2   # 规范化：折叠 // 与 /private/var 符号链接
export SEEK_PROVIDER_TEST_LOG="$tmp/invocations.log"
: > "$SEEK_PROVIDER_TEST_LOG"

server_pid=""
cleanup() {
  # wait 吞掉 bash 的 "Terminated" 作业通报，退出输出保持一行结论。
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null
    wait "$server_pid" 2>/dev/null
  fi
  rm -rf "eval/cases/$CASE1" "eval/cases/$CASE2" "$tmp"
  return 0
}
trap cleanup EXIT

results_before=$(find eval/results 2>/dev/null | sort)

# ---- 两个合成用例：界全过（1 turn / 1 agent_end），无 setup/teardown ----
mkdir -p "eval/cases/$CASE1"
printf 'what is the answer?\n' > "eval/cases/$CASE1/prompt.txt"
printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1}\n' > "eval/cases/$CASE1/expect.json"

# runner 用例：记录 env 契约视图后按转发形状自建调用（mcp-arm-runner
# 的接线同款——${SEEK_EVAL_PROVIDER_ARGS-} 未设/为空都安全）。
mkdir -p "eval/cases/$CASE2"
printf 'what is the answer?\n' > "eval/cases/$CASE2/prompt.txt"
printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1}\n' > "eval/cases/$CASE2/expect.json"
cat > "eval/cases/$CASE2/runner.sh" <<'RUNNER'
#!/usr/bin/env bash
set -u
{
  echo "=== runner-env"
  echo "rargs:${SEEK_EVAL_PROVIDER_ARGS-"<RUNNER-UNSET>"}"
  echo "renv:DEEPSEEK_API_KEY=${DEEPSEEK_API_KEY-"<RUNNER-UNSET>"}"
  echo "renv:OPENCODE_API_KEY=${OPENCODE_API_KEY-"<RUNNER-UNSET>"}"
} >> "${SEEK_PROVIDER_TEST_LOG:-/dev/null}"
prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
"$SEEK_EVAL_BINARY" -yolo -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" \
  -json ${SEEK_EVAL_PROVIDER_ARGS-} -p "$prompt"
RUNNER

# ---- 假二进制：-version 固定串；其余记录 argv+env 视图，回放固定流 ----
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/bin/bash
# fake seek binary — logs argv + provider-lane env view, fixed stream
set -u
log=${SEEK_PROVIDER_TEST_LOG:-/dev/null}
if [ "${1:-}" = "-version" ]; then
  { echo "=== version"; } >> "$log"
  echo "v9.9.9 · deadbee"
  exit 0
fi
{
  echo "=== invocation"
  echo "argv:$*"
  echo "env:DEEPSEEK_API_KEY=${DEEPSEEK_API_KEY-"<UNSET>"}"
  echo "env:OPENCODE_API_KEY=${OPENCODE_API_KEY-"<UNSET>"}"
  echo "env:OPENAI_API_KEY=${OPENAI_API_KEY-"<UNSET>"}"
  echo "env:OPENCODE_BASE_URL=${OPENCODE_BASE_URL-"<UNSET>"}"
  echo "env:SEEK_EVAL_PROVIDER_ARGS=${SEEK_EVAL_PROVIDER_ARGS-"<UNSET>"}"
} >> "$log"
cat <<'EOF'
{"type":"text_delta","delta":"lane answer","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":10,"completion_tokens":2,"cache_hit_tokens":0,"session_id":"fake-s1"}
EOF
exit 0
OUTER
chmod +x "$fake"

# snapshot → 跑一条腿 → 取本腿新增的日志切片（免解析整份累积日志）。
snap() { wc -l < "$SEEK_PROVIDER_TEST_LOG" | tr -d ' '; }
slice() { # slice FROM_LINE — 本腿新增行
  sed -n "$(( $1 + 1 )),\$p" "$SEEK_PROVIDER_TEST_LOG"
}

CANARY=seek-eval-metered-canary

# ---- ① 配置读取（假二进制；四旋钮映射进 argv 与导出 env）--------------
# 腿 A：车道关（历史行为）——argv 无 -provider；env 侧计量 key 原样可见
#       （擦除只发生在车道开启时，历史车道分毫不动）。
s=$(snap)
env -u SEEK_EVAL_PROVIDER -u SEEK_EVAL_MODEL -u SEEK_EVAL_BASE_URL -u SEEK_EVAL_API_KEY \
  DEEPSEEK_API_KEY="$CANARY" SEEK_EVAL_RESULTS_DIR="$tmp/ra" \
  bash eval/run.sh "$CASE1" "$fake" > "$tmp/legA.out" 2> "$tmp/legA.err" \
  || fail "leg A (lane off) run.sh exited non-zero"
blk=$(slice "$s")
case "$blk" in
  *"-provider"*) fail "leg A: lane off but argv carries -provider: $blk" ;;
esac
grep -q "env:DEEPSEEK_API_KEY=$CANARY" <<<"$blk" \
  || fail "leg A: historical lane must still see DEEPSEEK_API_KEY (scrub is lane-scoped): $blk"

# 腿 B：opencode 车道，仅 provider+key —— argv 只有 -provider opencode；
#       凭证导出为 OPENCODE_API_KEY；计量 key 被擦；ARGS 契约变量导出。
s=$(snap)
SEEK_EVAL_PROVIDER=opencode SEEK_EVAL_API_KEY=fake-lane-key \
  DEEPSEEK_API_KEY="$CANARY" SEEK_EVAL_RESULTS_DIR="$tmp/rb" \
  bash eval/run.sh "$CASE1" "$fake" > "$tmp/legB.out" 2> "$tmp/legB.err" \
  || fail "leg B (opencode) run.sh exited non-zero"
blk=$(slice "$s")
grep -q 'argv:.*-provider opencode' <<<"$blk" || fail "leg B: argv missing '-provider opencode': $blk"
case "$blk" in
  *"-model"*|*"-base-url"*) fail "leg B: unexpected -model/-base-url with knobs unset: $blk" ;;
esac
grep -q 'env:OPENCODE_API_KEY=fake-lane-key' <<<"$blk" || fail "leg B: OPENCODE_API_KEY not exported: $blk"
grep -q 'env:DEEPSEEK_API_KEY=<UNSET>' <<<"$blk" || fail "leg B: metered key not scrubbed: $blk"
grep -q 'env:SEEK_EVAL_PROVIDER_ARGS=-provider opencode' <<<"$blk" \
  || fail "leg B: SEEK_EVAL_PROVIDER_ARGS contract var not exported: $blk"

# 腿 C：opencode + model + base_url —— 三个 flag 全进 argv；
#       OPENCODE_BASE_URL 同步导出（config preset 劫持防线）。
s=$(snap)
SEEK_EVAL_PROVIDER=opencode SEEK_EVAL_MODEL=custom-model \
  SEEK_EVAL_BASE_URL=http://127.0.0.1:1/override/chat/completions \
  SEEK_EVAL_RESULTS_DIR="$tmp/rc" \
  bash eval/run.sh "$CASE1" "$fake" > "$tmp/legC.out" 2> "$tmp/legC.err" \
  || fail "leg C (opencode+knobs) run.sh exited non-zero"
blk=$(slice "$s")
grep -q 'argv:.*-provider opencode -model custom-model -base-url http://127.0.0.1:1/override/chat/completions' <<<"$blk" \
  || fail "leg C: argv not carrying all three provider flags: $blk"
grep -q 'env:OPENCODE_BASE_URL=http://127.0.0.1:1/override/chat/completions' <<<"$blk" \
  || fail "leg C: OPENCODE_BASE_URL not exported: $blk"

# 腿 D：compatible 车道 —— 凭证导出为 OPENAI_API_KEY（不是 OPENCODE）。
s=$(snap)
SEEK_EVAL_PROVIDER=compatible SEEK_EVAL_MODEL=fake-model-x \
  SEEK_EVAL_BASE_URL=http://127.0.0.1:1/compat/chat/completions \
  SEEK_EVAL_API_KEY=fake-compat-key SEEK_EVAL_RESULTS_DIR="$tmp/rd" \
  bash eval/run.sh "$CASE1" "$fake" > "$tmp/legD.out" 2> "$tmp/legD.err" \
  || fail "leg D (compatible) run.sh exited non-zero"
blk=$(slice "$s")
grep -q 'argv:.*-provider compatible -model fake-model-x' <<<"$blk" || fail "leg D: argv wrong: $blk"
grep -q 'env:OPENAI_API_KEY=fake-compat-key' <<<"$blk" || fail "leg D: OPENAI_API_KEY not exported: $blk"
grep -q 'env:OPENCODE_API_KEY=<UNSET>' <<<"$blk" || fail "leg D: wrong lane env var exported: $blk"

# ---- ④ 形状校验（exit 2 + 说明；在用例执行前就拒）----------------------
out=$(SEEK_EVAL_PROVIDER=compatible SEEK_EVAL_MODEL=m SEEK_EVAL_RESULTS_DIR="$tmp/re" \
  bash eval/run.sh "$CASE1" "$fake" 2>&1); rc=$?
[ "$rc" -eq 2 ] || fail "shape check: compatible w/o BASE_URL must exit 2, got $rc"
grep -q "SEEK_EVAL_BASE_URL" <<<"$out" || fail "shape check: error must name the missing knob: $out"
out=$(SEEK_EVAL_PROVIDER=nosuch SEEK_EVAL_RESULTS_DIR="$tmp/re" \
  bash eval/run.sh "$CASE1" "$fake" 2>&1); rc=$?
[ "$rc" -eq 2 ] || fail "shape check: unknown provider must exit 2, got $rc"
grep -q "unknown SEEK_EVAL_PROVIDER" <<<"$out" || fail "shape check: error must name the bad provider: $out"

# ---- ③ runner 转发契约（假二进制）--------------------------------------
# 车道开：runner 看到 ARGS 契约变量 + 车道凭证、看不到计量 key；
# 转发后的 argv 带 -provider。
s=$(snap)
SEEK_EVAL_PROVIDER=opencode SEEK_EVAL_API_KEY=fake-lane-key \
  DEEPSEEK_API_KEY="$CANARY" SEEK_EVAL_RESULTS_DIR="$tmp/rh" \
  bash eval/run.sh "$CASE2" "$fake" > "$tmp/legH.out" 2> "$tmp/legH.err" \
  || fail "runner leg (lane on) run.sh exited non-zero"
blk=$(slice "$s")
grep -q 'rargs:-provider opencode' <<<"$blk" || fail "runner leg: SEEK_EVAL_PROVIDER_ARGS not visible to runner: $blk"
grep -q 'renv:OPENCODE_API_KEY=fake-lane-key' <<<"$blk" || fail "runner leg: lane credential not visible to runner: $blk"
grep -q 'renv:DEEPSEEK_API_KEY=<RUNNER-UNSET>' <<<"$blk" || fail "runner leg: metered key visible to runner: $blk"
grep -q 'argv:.*-provider opencode' <<<"$blk" || fail "runner leg: forwarded invocation missing -provider: $blk"
# 车道关：ARGS 为空串（run.sh 显式传，非未设）、argv 不带 -provider。
s=$(snap)
env -u SEEK_EVAL_PROVIDER -u SEEK_EVAL_MODEL -u SEEK_EVAL_BASE_URL -u SEEK_EVAL_API_KEY \
  SEEK_EVAL_RESULTS_DIR="$tmp/ri" \
  bash eval/run.sh "$CASE2" "$fake" > "$tmp/legI.out" 2> "$tmp/legI.err" \
  || fail "runner leg (lane off) run.sh exited non-zero"
blk=$(slice "$s")
grep -q 'rargs:$' <<<"$blk" || fail "runner leg (off): SEEK_EVAL_PROVIDER_ARGS should be empty string: $blk"
case "$blk" in
  *"-provider"*) fail "runner leg (off): argv carries -provider: $blk" ;;
esac

# ---- ② 请求目标切换 + 计量 key 零引用（真二进制 × 假端点）---------------
go build -o "$tmp/seek-real" ./cmd/seek \
  || { echo "eval-provider selftest: go build failed" >&2; exit 2; }

PORT=$((20000 + RANDOM % 20000))
server_log="$tmp/server.log"
: > "$server_log"
PORT="$PORT" LOG="$server_log" python3 eval/tools/fake_openai_compat.py \
  > "$tmp/server.out" 2> "$tmp/server.err" &
server_pid=$!
up=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/" || true)
  [ "$code" = "501" ] && { up=1; break; }   # GET 未实现=服务器已在线（GET 不进请求日志）
  sleep 0.3
done
[ "$up" = "1" ] || { echo "eval-provider selftest: fake endpoint did not come up on :$PORT" >&2; cat "$tmp/server.err" >&2; exit 2; }
fake_url="http://127.0.0.1:$PORT/v1/chat/completions"

# 临时 SEEK_HOME：config 侧再埋一只计量 key 金丝雀 + opencode preset
# 劫持端点（端口 9 上，打到即败）——env 侧金丝雀在腿环境里埋。
home_dir="$tmp/seekhome"
mkdir -p "$home_dir"
cat > "$home_dir/config.json" <<'CFG'
{
  "providers": {
    "deepseek": {"api_key": "config-side-metered-canary"},
    "opencode": {"base_url": "http://127.0.0.1:9/preset-hijack/chat/completions"}
  }
}
CFG

# 腿 F：compatible 车道全旋钮 —— 请求打到假端点、Bearer 是车道假 key、
#       model 上线、两处金丝雀零泄漏、结果行 PASS 且终答来自假端点。
SEEK_HOME="$home_dir" \
SEEK_EVAL_PROVIDER=compatible \
SEEK_EVAL_BASE_URL="$fake_url" \
SEEK_EVAL_MODEL=fake-model-x \
SEEK_EVAL_API_KEY=fake-lane-key \
DEEPSEEK_API_KEY="$CANARY" \
SEEK_EVAL_RESULTS_DIR="$tmp/rf" \
  bash eval/run.sh "$CASE1" "$tmp/seek-real" > "$tmp/legF.out" 2> "$tmp/legF.err" \
  || fail "leg F (real compatible) run.sh exited non-zero"
row=$(jq -c 'select(.case=="'"$CASE1"'")' "$tmp/rf/"*.jsonl 2>/dev/null | tail -1)
[ "$(jq -r '.pass' <<<"$row")" = "true" ] || fail "leg F: case row not PASS: $row"
jq -e '.final_text | contains("LANE OK")' <<<"$row" > /dev/null \
  || fail "leg F: final_text lacks fake-endpoint answer (stream not consumed): $row"
[ "$(wc -l < "$server_log" | tr -d ' ')" -ge 1 ] || fail "leg F: fake endpoint saw no POST"
f_rec=$(tail -1 "$server_log")
jq -e '.path == "/v1/chat/completions"' <<<"$f_rec" > /dev/null || fail "leg F: request path wrong: $f_rec"
jq -e '.auth == "Bearer fake-lane-key"' <<<"$f_rec" > /dev/null || fail "leg F: auth not the lane key: $f_rec"
jq -e '.model == "fake-model-x"' <<<"$f_rec" > /dev/null || fail "leg F: model knob not on the wire: $f_rec"
grep -q canary "$server_log" && fail "leg F: metered-key canary reached the endpoint"

# 腿 G：opencode 车道不传 -model —— 线上缺省即 deepseek-v4.1-flash；
#        config preset 劫持被 OPENCODE_BASE_URL 导出压过（请求仍到假端点）。
SEEK_HOME="$home_dir" \
SEEK_OPENCODE_CATALOG=0 \
SEEK_EVAL_PROVIDER=opencode \
SEEK_EVAL_BASE_URL="$fake_url" \
SEEK_EVAL_API_KEY=fake-key-2 \
DEEPSEEK_API_KEY="$CANARY" \
SEEK_EVAL_RESULTS_DIR="$tmp/rg" \
  bash eval/run.sh "$CASE1" "$tmp/seek-real" > "$tmp/legG.out" 2> "$tmp/legG.err" \
  || fail "leg G (real opencode) run.sh exited non-zero"
row=$(jq -c 'select(.case=="'"$CASE1"'")' "$tmp/rg/"*.jsonl 2>/dev/null | tail -1)
[ "$(jq -r '.pass' <<<"$row")" = "true" ] || fail "leg G: case row not PASS: $row"
g_rec=$(tail -1 "$server_log")
jq -e '.path == "/v1/chat/completions"' <<<"$g_rec" > /dev/null \
  || fail "leg G: request did not reach the fake endpoint (config preset hijack won?): $g_rec"
jq -e '.auth == "Bearer fake-key-2"' <<<"$g_rec" > /dev/null || fail "leg G: auth not the lane key: $g_rec"
jq -e '.model == "deepseek-v4.1-flash"' <<<"$g_rec" > /dev/null \
  || fail "leg G: opencode lane default model on the wire must be deepseek-v4.1-flash: $g_rec"
grep -q canary "$server_log" && fail "leg G: metered-key canary reached the endpoint"
grep -q "preset-hijack" "$server_log" && fail "leg G: preset hijack endpoint leaked into a request"

# ---- 全程零污染：eval/results/ 清单跑前跑后逐字节相等 --------------------
results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] \
  || fail "eval/results inventory changed (provider-lane test must never write trend data)"

if [ "$fails" -eq 0 ]; then
  echo "eval-provider selftest: PASS (knob→argv/env mapping + fake-endpoint request-target/credential proofs + runner contract + shape checks)"
  exit 0
fi
echo "eval-provider selftest: FAIL (${fails} assertion(s))" >&2
exit 1
