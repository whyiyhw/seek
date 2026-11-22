#!/bin/bash
# eval-judge-test.sh — eval v3 J1 判分层（LLM-as-judge over final_text）
# 的确定性自测。scripts/*.sh 自动进 selftest 发现制名单，裸跑即自测。
#
# 为什么存在：同 eval-rounds-test.sh 的出生教训——判分层的验收若只靠
# 沙盒手跑，事后无法机械复跑。本脚本把 J1 的判定力学钉死：
#
#   ① record 模式 + fail 判决：行上三键齐（judge_verdict/fail、
#      judge_reason、judge_model）但 pass=true 不染色、exit 0；
#   ② gate 模式 + fail 判决：failures 含 "judge: …"、pass=false、
#      exit 1；
#   ③ gate 模式 + 非 JSON 输出：verdict=doubt，两向都不染色（判分
#      层铁律：judge 的任何失败形态折叠为 doubt，永不假绿——
#      internal/verifier/judge.go 的 no-JSON=never-green 同款）；
#   ④ 无 judge 块的用例：行上零 judge 键（data-gated，字节形状不变
#      ——ablate/lane/rounds 三个字节 pin 的共同前提）；
#   ⑤ 畸形 judge 块（mode 非法）：启动 exit 2、零行写入（同
#      min_pass_rate 的 read_gate_config 纪律）；
#   ⑥ 全局 kill-switch SEEK_EVAL_JUDGE=0：声明了 judge 块也不判、
#      零 judge 键、假二进制日志零 judge 调用；
#   ⑦ 真二进制 × 本地假 OpenAI 兼容端点：SUT 调用与 judge 调用打同
#      一端点，线上 model 分别为车道缺省 deepseek-v4.1-flash 与
#      SEEK_EVAL_JUDGE_MODEL——一次 run 两条 POST、两个模型，证明
#      判分模型旋钮真的到了线上（eval-provider-test 的真二进制腿
#      同款手法）；DELTAS 喂判决 JSON，行上 judge_verdict=fail。
#
# macOS bash 3.2 兼容（selftest 通道全仓口径）：无 mapfile / 关联数组；
# ${VAR} 后紧跟非 ASCII 字符一律加花括号。⑦ 需要 go 构建真二进制，
# 与 eval-provider-test 同款依赖声明。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-judge selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-judge selftest: jq required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "eval-judge selftest: python3 required" >&2; exit 2; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-judge-selftest.XXXXXX")
tmp=$(cd "$tmp" && pwd) || exit 2

server_pid=""
cleanup() {
  if [ -n "$server_pid" ]; then
    kill "$server_pid" 2>/dev/null
    wait "$server_pid" 2>/dev/null
  fi
  rm -rf "$tmp"
  return 0
}
trap cleanup EXIT

# ---- 沙盒仓：run.sh + judge.py 副本 + 四个合成用例 ----------------------
sb="$tmp/sandbox"
mkdir -p "$sb/eval/tools" "$sb/eval/cases"
cp eval/run.sh "$sb/eval/run.sh"
cp eval/tools/judge.py "$sb/eval/tools/judge.py"

mkcase() { # mkcase NAME JUDGE_JSON_OR_EMPTY
  mkdir -p "$sb/eval/cases/$1"
  printf 'answer the question\n' > "$sb/eval/cases/$1/prompt.txt"
  if [ -n "$2" ]; then
    printf '{"max_turns":3,%s}\n' "$2" > "$sb/eval/cases/$1/expect.json"
  else
    printf '{"max_turns":3}\n' > "$sb/eval/cases/$1/expect.json"
  fi
}
ZJ_REC=zjudge-rec
ZJ_GATE=zjudge-gate
ZJ_NOISE=zjudge-noise
ZJ_PLAIN=zjudge-plain
ZJ_BAD=zjudge-bad
ZJ_WIRE=zjudge-wire
mkcase "$ZJ_REC"  '"judge":{"rubric":"answer must be correct","mode":"record","model":"zj-fail"}'
mkcase "$ZJ_GATE" '"judge":{"rubric":"answer must be correct","mode":"gate","model":"zj-fail"}'
mkcase "$ZJ_NOISE" '"judge":{"rubric":"answer must be correct","mode":"gate","model":"zj-noise"}'
mkcase "$ZJ_PLAIN" ''
mkcase "$ZJ_BAD"  '"judge":{"rubric":"answer must be correct","mode":"strict"}'
# ⑦ 专用：不带 model——判分模型必须落到 env 的 SEEK_EVAL_JUDGE_MODEL。
mkcase "$ZJ_WIRE" '"judge":{"rubric":"answer must be correct","mode":"gate"}'

# ---- 假二进制：-version 固定串；argv 带 -model <zj-…> 即判分调用 -------
# 判分调用按 model 标记出不同判决流；SUT 调用出带 final 文本的好流。
# 每次调用全量 argv 落 $FAKELOG，⑥ 据此断言 kill-switch 下零判分调用。
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/bin/bash
set -u
if [ "${1:-}" = "-version" ]; then
  echo "v9.9.9 · judgebeef"
  exit 0
fi
echo "$*" >> "${FAKELOG:-/dev/null}"
model=""
p=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-model" ]; then model=$a; fi
  if [ "$prev" = "-p" ]; then p=$a; fi
  prev=$a
done
case "$model" in
  zj-fail)
    cat <<'EOF'
{"type":"text_delta","delta":"{\"pass\": false, \"reason\": \"canned fail\"}","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":50,"completion_tokens":9,"cache_hit_tokens":0,"session_id":"judge-r"}
EOF
    ;;
  zj-noise)
    cat <<'EOF'
{"type":"text_delta","delta":"I looked at it and it seems fine overall, though maybe check again.","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":50,"completion_tokens":9,"cache_hit_tokens":0,"session_id":"judge-r"}
EOF
    ;;
  zj-dynamic)
    # ⑧ calibrate 腿：按 prompt 内嵌标记分流（judge prompt 含 final_text 原文）
    case "$p" in
      *CALIB-PASS-MARKER*)
        printf '%s\n' '{"type":"text_delta","delta":"{\"pass\": true, \"reason\": \"canned pass\"}","reasoning":false}'
        ;;
      *CALIB-NOISE-MARKER*)
        printf '%s\n' '{"type":"text_delta","delta":"definitely looks right to me, no further notes.","reasoning":false}'
        ;;
      *)
        printf '%s\n' '{"type":"text_delta","delta":"{\"pass\": false, \"reason\": \"canned fail\"}","reasoning":false}'
        ;;
    esac
    printf '%s\n' '{"type":"turn_end"}'
    printf '%s\n' '{"type":"agent_end","prompt_tokens":50,"completion_tokens":9,"cache_hit_tokens":0,"session_id":"judge-r"}'
    ;;
  *)
    cat <<'EOF'
{"type":"tool_start","name":"read","args":"{}"}
{"type":"tool_end","name":"read","result":"ok"}
{"type":"text_delta","delta":"SUT final answer text","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":900,"completion_tokens":40,"cache_hit_tokens":300,"session_id":"sut-r"}
EOF
    ;;
esac
exit 0
OUTER
chmod +x "$fake"

FAKELOG="$tmp/fakelog"
: > "$FAKELOG"

run_eval() { # run_eval RESDIR CASE EXTRA_ENV...（KEY=VAL 对）
  local resdir=$1 c=$2; shift 2
  : > "$FAKELOG"
  # shellcheck disable=SC2090 # 需要字面量 KEY=VAL 对经 env 透传
  env "$@" FAKELOG="$FAKELOG" SEEK_EVAL_RESULTS_DIR="$resdir" \
    bash "$sb/eval/run.sh" "$c" "$fake" 2>"$tmp/stderr"
}

results_before=$(find eval/results 2>/dev/null | sort)

# ---- ① record + fail：不染色，三键齐 ------------------------------------
d="$tmp/b1"; mkdir -p "$d"
run_eval "$d" "$ZJ_REC" >/dev/null; rc=$?
[ "$rc" -eq 0 ] || fail "① record+fail exited ${rc} (want 0)"
row=$(jq -c "select(.case==\"$ZJ_REC\")" "$d"/*.jsonl)
[ "$(jq -r '.pass' <<<"$row")" = "true" ] || fail "① record+fail coloured the row (pass must stay true)"
[ "$(jq -r '.judge_verdict' <<<"$row")" = "fail" ] || fail "① judge_verdict not recorded as fail"
[ "$(jq -r '.judge_model' <<<"$row")" = "zj-fail" ] || fail "① judge_model not the case-level override"
[ -n "$(jq -r '.judge_reason' <<<"$row")" ] || fail "① judge_reason empty"

# ---- ② gate + fail：染色，exit 1 ----------------------------------------
d="$tmp/b2"; mkdir -p "$d"
run_eval "$d" "$ZJ_GATE" >/dev/null; rc=$?
[ "$rc" -eq 1 ] || fail "② gate+fail exited ${rc} (want 1)"
row=$(jq -c "select(.case==\"$ZJ_GATE\")" "$d"/*.jsonl)
[ "$(jq -r '.pass' <<<"$row")" = "false" ] || fail "② gate+fail did not colour the row"
jq -e '[.failures[] | select(startswith("judge:"))] | length > 0' <<<"$row" >/dev/null \
  || fail "② failures missing the judge: line"

# ---- ③ gate + 非 JSON：doubt，不染色 ------------------------------------
d="$tmp/b3"; mkdir -p "$d"
run_eval "$d" "$ZJ_NOISE" >/dev/null; rc=$?
[ "$rc" -eq 0 ] || fail "③ gate+noise exited ${rc} (want 0 — doubt never colours)"
row=$(jq -c "select(.case==\"$ZJ_NOISE\")" "$d"/*.jsonl)
[ "$(jq -r '.pass' <<<"$row")" = "true" ] || fail "③ doubt coloured the row"
[ "$(jq -r '.judge_verdict' <<<"$row")" = "doubt" ] || fail "③ unparseable output not folded to doubt"

# ---- ④ 无 judge 块：零 judge 键（data-gated）-----------------------------
d="$tmp/b4"; mkdir -p "$d"
run_eval "$d" "$ZJ_PLAIN" >/dev/null; rc=$?
[ "$rc" -eq 0 ] || fail "④ plain case exited ${rc} (want 0)"
row=$(jq -c "select(.case==\"$ZJ_PLAIN\")" "$d"/*.jsonl)
jq -e 'has("judge_verdict") or has("judge_reason") or has("judge_model") | not' <<<"$row" >/dev/null \
  || fail "④ plain row carries judge keys (must be data-gated away)"

# ---- ⑤ 畸形 judge 块：exit 2 零行 ----------------------------------------
d="$tmp/b5"; mkdir -p "$d"
run_eval "$d" "$ZJ_BAD" >/dev/null; rc=$?
[ "$rc" -eq 2 ] || fail "⑤ malformed judge block exited ${rc} (want 2)"
rows=$(cat "$d"/*.jsonl 2>/dev/null | wc -l | tr -d ' ')
[ "$rows" -eq 0 ] || fail "⑤ malformed judge block wrote ${rows} rows (want 0)"
grep -q 'judge.mode must be' "$tmp/stderr" || fail "⑤ stderr missing the judge.mode diagnostic"

# ---- ⑥ kill-switch：声明了也不判 -----------------------------------------
d="$tmp/b6"; mkdir -p "$d"
run_eval "$d" "$ZJ_GATE" SEEK_EVAL_JUDGE=0 >/dev/null; rc=$?
[ "$rc" -eq 0 ] || fail "⑥ kill-switch run exited ${rc} (want 0)"
row=$(jq -c "select(.case==\"$ZJ_GATE\")" "$d"/*.jsonl)
jq -e 'has("judge_verdict") | not' <<<"$row" >/dev/null || fail "⑥ kill-switch row still judged"
grep -q -- '-model zj-fail' "$FAKELOG" && fail "⑥ kill-switch still made a judge call"

# ---- ⑦ 真二进制 × 假端点：线上模型分离 + 全链接线 ------------------------
# SUT 调用（无 -model）线上是车道缺省 deepseek-v4.1-flash；judge 调用线上
# 是 SEEK_EVAL_JUDGE_MODEL=zjudge-wire。DELTAS 喂 fail 判决 JSON——SUT 的
# final_text 与 judge 的输入同为该串，判分链路把它折成 judge_verdict=fail。
if command -v go >/dev/null 2>&1; then
  go build -o "$tmp/seek-real" ./cmd/seek \
    || { echo "eval-judge selftest: go build failed" >&2; exit 2; }
  port=$((20000 + RANDOM % 20000))
  LOGF="$tmp/endpoint.jsonl"
  : > "$LOGF"
  PORT="$port" LOG="$LOGF" DELTAS='{"pass": false, "reason": "wire canned fail"}' \
    python3 eval/tools/fake_openai_compat.py >/dev/null 2>&1 &
  server_pid=$!
  up=0
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/" || true)
    [ "$code" = "501" ] && { up=1; break; }   # GET 未实现=服务器已在线（GET 不进请求日志）
    sleep 0.3
  done
  [ "$up" = "1" ] || { echo "eval-judge selftest: fake endpoint did not come up on :$port" >&2; exit 2; }
  d="$tmp/b7"; mkdir -p "$d"
  env SEEK_EVAL_PROVIDER=opencode \
      SEEK_EVAL_BASE_URL="http://127.0.0.1:${port}/v1/chat/completions" \
      SEEK_EVAL_API_KEY=fake \
      SEEK_EVAL_JUDGE_MODEL=zjudge-wire \
      SEEK_EVAL_RESULTS_DIR="$d" \
      FAKELOG="$FAKELOG" \
      bash "$sb/eval/run.sh" "$ZJ_WIRE" "$tmp/seek-real" >/dev/null 2>"$tmp/stderr7"; rc=$?
  [ "$rc" -eq 1 ] || fail "⑦ wire leg exited ${rc} (want 1 — gated fail)"
  row=$(jq -c "select(.case==\"$ZJ_WIRE\")" "$d"/*.jsonl 2>/dev/null)
  [ "$(jq -r '.judge_verdict // empty' <<<"$row")" = "fail" ] || fail "⑦ judge_verdict not fail on the wire leg"
  [ "$(jq -r '.judge_model // empty' <<<"$row")" = "zjudge-wire" ] || fail "⑦ judge_model not zjudge-wire"
  models=$(jq -r '.model' "$LOGF" 2>/dev/null | sort -u)
  echo "$models" | grep -q '^deepseek-v4.1-flash$' || fail "⑦ SUT call did not ride the lane default model (log: $(echo "$models" | tr '\n' ' '))"
  echo "$models" | grep -q '^zjudge-wire$' || fail "⑦ judge call did not ride the judge model (log: $(echo "$models" | tr '\n' ' '))"
  kill "$server_pid" 2>/dev/null; wait "$server_pid" 2>/dev/null; server_pid=""
else
  echo "eval-judge selftest: WARN: go missing — skipping ⑦ (real-binary wire leg)" >&2
fi

# ---- ⑧ calibrate 机制：3 行迷你校准集走真 judge.py + 假二进制 ------------
# 分流按 final_text 内嵌标记（judge prompt 含 final_text 原文）：PASS 标记→
# 判 pass、NOISE 标记→非 JSON（doubt）、无标记→判 fail。三行标签
# pass/fail/pass → agree=2、label-pass→doubt 一例、false-pass=0、
# agreement=0.67 → gate-eligible=no（阈值逻辑走真路径）。
caldir="$tmp/calib"
mkdir -p "$caldir"
# 行序：1=pass（PASS 标记→判 pass）、2=pass（NOISE 标记→非 JSON doubt）、3=fail（无标记→判 fail）
jq -cn --argjson id 1 --arg label pass '{id:$id, case:"zcalib-a", origin:"real", ran_at:"", label:$label, label_note:"", rubric:"answer must be correct", prompt:"q", final_text:"CALIB-PASS-MARKER good answer"}' > "$caldir/fixtures.jsonl"
jq -cn --argjson id 2 --arg label pass '{id:$id, case:"zcalib-b", origin:"real", ran_at:"", label:$label, label_note:"", rubric:"answer must be correct", prompt:"q", final_text:"CALIB-NOISE-MARKER unverifiable hedging"}' >> "$caldir/fixtures.jsonl"
jq -cn --argjson id 3 --arg label fail '{id:$id, case:"zcalib-c", origin:"real", ran_at:"", label:$label, label_note:"", rubric:"answer must be correct", prompt:"q", final_text:"a wrong answer"}' >> "$caldir/fixtures.jsonl"
SEEK_EVAL_BINARY="$fake" SEEK_EVAL_JUDGE_MODEL=zj-dynamic FAKELOG="$FAKELOG" \
  bash eval/tools/judge-calibrate.sh "$caldir/fixtures.jsonl" > "$tmp/calib.out" 2>"$tmp/calib.err"; rc=$?
[ "$rc" -eq 0 ] || fail "⑧ calibrate exited ${rc} (want 0): $(tail -3 "$tmp/calib.err")"
[ "$(wc -l < "$caldir/verdicts.jsonl" | tr -d ' ')" = "3" ] || fail "⑧ verdicts not 3 rows"
[ "$(jq -s 'map(select(.agree)) | length' "$caldir/verdicts.jsonl")" = "2" ] || fail "⑧ agree count not 2"
[ "$(jq -s 'map(select(.label=="fail" and .verdict=="pass")) | length' "$caldir/verdicts.jsonl")" = "0" ] || fail "⑧ false-pass not 0"
v_noise=$(jq -r 'select(.label=="pass") | select(.verdict=="doubt") | .verdict' "$caldir/verdicts.jsonl" | head -1)
[ "$v_noise" = "doubt" ] || fail "⑧ noise row not folded to doubt"
grep -q 'gate-eligible no\|gate-eligible  *no' "$caldir/report.md" || grep -q 'gate eligibility.*no' "$caldir/report.md" \
  || fail "⑧ report missing the not-eligible verdict (agreement 0.67 + false-pass 0)"
grep -q 'agreement: 0\.67' "$caldir/report.md" || fail "⑧ report agreement not 0.67"

results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] || fail "eval/results polluted by the selftest"

if [ "$fails" -eq 0 ]; then
  echo "eval-judge selftest: PASS (8 legs)"
  exit 0
fi
echo "eval-judge selftest: FAIL (${fails} failures)" >&2
exit 1
