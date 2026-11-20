#!/bin/bash
# eval-rounds-test.sh — eval/run.sh --rounds / min_pass_rate / record_only
# 的确定性自测（eval v2 E1，c_ennnwa，2026-11-20；scripts/*.sh 自动进
# selftest 发现制名单，裸跑即自测，零登记）。
#
# 为什么存在：同 eval-ablate-test.sh 的出生事故——E1 的验收若只靠沙盒
# 手跑假二进制，事后无法机械复跑。本脚本把验收收进受审代码，用按调用
# 次数切换事件流的假二进制钉死：
#
#   ① 显式 --rounds 1 与不传 flag 字节等价（归一化后 cmp stdout+stderr+
#      结果行）。「不传 flag 与 E1 改动前等价」由 eval-ablate-test 的
#      历史 blob 对照继续钉——两测合起来给出：--rounds 1 ≡ 无 flag ≡ 历史。
#   ② --rounds 5 聚合：每轮各落一行（round 字段 1..5）；假二进制第 4、5
#      次调用输出「无 agent_end 的崩溃流」→ 该轮按界判红且计入分母
#      （PRD §六 开放问题 1 的 E1 取向：崩溃轮 = 红轮，不设独立 error
#      类、不引入 max_error_rate——那是 E2+ 的决定）；
#   ③ min_pass_rate 边界各一例：3/5 对 0.6（含边界）判绿、3/5 对 0.7
#      判红——钉死「≥ 含边界」口径。PRD §三 E1 行原文「0.6 在 3/5 时判红」
#      按字面是严格不等号，但与 §2.1「取 2/3」自相矛盾（0.67 ≠ 2/3）；
#      实现取 ≥（IEEE 除法与字面量解析舍入到同一 double，3/5 与 0.6
#      精确相等），理由与 E2 标定建议（取恰为 k/n 的阈值）见
#      eval/README.md §Judgment；
#   ④ record_only：只记录不判红不判绿——RECORD 行、exit 0、汇总分列
#      recorded、聚合行 verdict=record；
#   ⑤ 畸形输入启动即 exit 2 且零行写入：--rounds 0 / --rounds abc /
#      min_pass_rate 越界（1.5、-0.5、0）/ 类型错（字符串）/ record_only
#      非布尔（PRD §四：不许静默当 1.0/false）；
#   ⑥ reader 兼容：带 round 字段的轮次行 + 聚合行喂给
#      scripts/eval-trend.sh —— 聚合行不双计（runs == 轮次行数）。
#
# 中断（Ctrl-C 半程）不注入信号测试：macOS bash 3.2 下信号注入不定
# （SIGINT 的 deferral 语义随等待状态变化）。「已完成轮次不丢」由结构
# 保证——每轮完成后才追加该轮行、单行单 echo（无半行 JSONL 可产生）；
# 崩溃轮路径由 ② 覆盖。若未来改行写入方式，先回来补信号注入测试。
#
# macOS bash 3.2 兼容（selftest 通道全仓口径）：无 mapfile / 关联数组；
# ${VAR} 后紧跟非 ASCII 字符一律加花括号。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-rounds selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-rounds selftest: jq required" >&2; exit 2; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-rounds-selftest.XXXXXX")
# 规范化（cd+pwd）：macOS TMPDIR 带尾斜杠 / /var 符号链接，与 run.sh
# 打印的 cd+pwd 物理路径对齐，否则 sed 归一化静默不命中（同 ablate 自测）。
tmp=$(cd "$tmp" && pwd) || exit 2

cleanup() {
  rm -rf "$tmp"
  return 0
}
trap cleanup EXIT

CASE=zrounds-gate
REC=zrounds-record

# ---- 沙盒仓：run.sh 副本 + 两个合成用例（零副作用，界可绿可红） ------
sb="$tmp/sandbox"
mkdir -p "$sb/eval/cases/$CASE" "$sb/eval/cases/$REC"
cp eval/run.sh "$sb/eval/run.sh"
printf 'what is the answer?\n' > "$sb/eval/cases/$CASE/prompt.txt"
printf 'what is the answer?\n' > "$sb/eval/cases/$REC/prompt.txt"
# exact_agent_ends=1：好流过界；崩溃流（无 agent_end、零 turn）判红——
# 同一界既当通过判据又当「轮崩溃计入分母」的抓手。
printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1}\n' > "$sb/eval/cases/$CASE/expect.json"
printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1,"record_only":true}\n' > "$sb/eval/cases/$REC/expect.json"

# ---- 假二进制：-version 固定版本串；按调用序号前 GOOD_N 次出好流 ------
# （之后出「崩溃流」：无 tool/turn/agent_end 事件）。STATE/GOOD_N 经环境
# 传入，heredoc 单引号保运行期展开。
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/bin/bash
set -u
if [ "${1:-}" = "-version" ]; then
  echo "v9.9.9 · roundsbeef"
  exit 0
fi
n=$(cat "$STATE" 2>/dev/null || echo 0)
n=$((n+1)); echo "$n" > "$STATE"
if [ "$n" -le "$GOOD_N" ]; then
  cat <<'EOF'
{"type":"tool_start","name":"read","args":"{}"}
{"type":"tool_end","name":"read","result":"ok"}
{"type":"text_delta","delta":"round answer","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":900,"completion_tokens":40,"cache_hit_tokens":300,"session_id":"fake-r"}
EOF
else
  cat <<'EOF'
{"type":"text_delta","delta":"crashed mid-flight","reasoning":false}
EOF
fi
exit 0
OUTER
chmod +x "$fake"

run_eval() { # run_eval RESULTS_DIR EXTRA_ARGS... —— 透传给沙盒 run.sh
  local resdir=$1; shift
  export STATE="$tmp/state"
  SEEK_EVAL_RESULTS_DIR="$resdir" bash "$sb/eval/run.sh" "$@" "$CASE" "$fake" 2>"$tmp/stderr"
}

results_before=$(find eval/results 2>/dev/null | sort)

# ---- ①：显式 --rounds 1 ≡ 不传 flag（归一化字节等价） --------------------
norm() { # norm SUBDIR —— 归一化只动「同一脚本连跑两次也会变」的字段
  sed -E \
    -e "s|$tmp/$1|ROOT|g" \
    -e "s|$tmp|TMP|g" \
    -e 's|20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z|TS|g' \
    -e 's|20[0-9]{2}-[0-9]{2}-[0-9]{2}-v9\.9\.9-roundsbeef|DATE-v9.9.9-roundsbeef|g' \
    -e 's|\([0-9]+s,|(Xs,|g' \
    -e 's|elapsed_s":[0-9]+|elapsed_s":E|g'
}
export GOOD_N=99
for side in noflag r1; do
  mkdir -p "$tmp/b1/$side"
  : > "$tmp/state"
  if [ "$side" = "noflag" ]; then
    run_eval "$tmp/b1/$side" > "$tmp/b1/$side.out"
  else
    run_eval "$tmp/b1/$side" --rounds 1 > "$tmp/b1/$side.out"
  fi
  rc=$?
  [ "$rc" -eq 0 ] || fail "① $side run exited ${rc} (want 0)"
  cat "$tmp/b1/$side.out" "$tmp/stderr" "$tmp/b1/$side/"*.jsonl | norm "b1/$side" > "$tmp/b1/$side.norm"
  # 空产物防御：sed 出错两侧同空 → cmp 假绿（ablate 自测同款教训）。
  [ -s "$tmp/b1/$side.norm" ] || fail "① $side normalized output empty (norm/sed broken?)"
done
cmp -s "$tmp/b1/noflag.norm" "$tmp/b1/r1.norm" \
  || fail "① explicit --rounds 1 differs from no-flag run: $(diff "$tmp/b1/noflag.norm" "$tmp/b1/r1.norm" | head -5)"

# ---- ②③：--rounds 5 聚合 + 边界各一例 ------------------------------------
# 同一分布（前 3 轮好、后 2 轮崩溃 → 3/5）：对 0.6 含边界判绿、对 0.7 判红。
export GOOD_N=3
mkdir -p "$tmp/agreen" "$tmp/ared"
printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1,"min_pass_rate":0.6}\n' > "$sb/eval/cases/$CASE/expect.json"
: > "$tmp/state"
run_eval "$tmp/agreen" --rounds 5 > "$tmp/agreen.out" 2>/dev/null
rc=$?
[ "$rc" -eq 0 ] || fail "③ 3/5 vs min 0.6 (inclusive boundary) exited ${rc} (want 0)"
grep -q '^PASS  zrounds-gate  aggregate (3/5 rounds >= min_pass_rate 0.6)$' "$tmp/agreen.out" \
  || fail "③ missing/incorrect boundary-green aggregate line: $(grep aggregate "$tmp/agreen.out")"

printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1,"min_pass_rate":0.7}\n' > "$sb/eval/cases/$CASE/expect.json"
: > "$tmp/state"
run_eval "$tmp/ared" --rounds 5 > "$tmp/ared.out" 2>/dev/null
rc=$?
[ "$rc" -eq 1 ] || fail "③ 3/5 vs min 0.7 exited ${rc} (want 1)"
grep -q '^FAIL  zrounds-gate  aggregate (3/5 rounds < min_pass_rate 0.7)$' "$tmp/ared.out" \
  || fail "③ missing/incorrect boundary-red aggregate line: $(grep aggregate "$tmp/ared.out")"

green_rows="$tmp/agreen/"*.jsonl
n_round_rows=$(jq -s '[.[] | select(.type != "aggregate")] | length' $green_rows)
[ "$n_round_rows" = "5" ] || fail "② want 5 per-round rows, got ${n_round_rows}"
n_round_fields=$(jq -s '[.[] | select(.type != "aggregate" and .round != null)] | length' $green_rows)
[ "$n_round_fields" = "5" ] || fail "② per-round rows missing the round field (${n_round_fields}/5)"
rounds_seq=$(jq -s -r '[.[] | select(.type != "aggregate") | .round] | sort | join(",")' $green_rows)
[ "$rounds_seq" = "1,2,3,4,5" ] || fail "② round values wrong: ${rounds_seq}"
crash_fail_rows=$(jq -s '[.[] | select(.type != "aggregate" and .round >= 4 and .pass == false)] | length' $green_rows)
[ "$crash_fail_rows" = "2" ] || fail "② crashed rounds (4,5) must each judge red, got ${crash_fail_rows}/2 — denominator must include crashes"
agg=$(jq -s '[.[] | select(.type == "aggregate")] | length' $green_rows)
[ "$agg" = "1" ] || fail "② want exactly 1 aggregate row, got ${agg}"
jq -e -s 'any(.[]; .type == "aggregate" and .verdict == "pass" and .pass_rate == 0.6 and .rounds == 5 and .passed == 3 and .record_only == false)' $green_rows >/dev/null \
  || fail "② aggregate row fields wrong: $(jq -c 'select(.type == "aggregate")' $green_rows)"
# 无 flag 路径不写 round/aggregate（字节等价已被①钉死，这里补钉行级断言）。
grep -q '"round"' "$tmp/b1/noflag/"*.jsonl && fail "① no-flag row must not carry a round field"
grep -q '"aggregate"' "$tmp/b1/noflag/"*.jsonl && fail "① no-flag run must not write an aggregate row"

# ---- ④：record_only 只记录，不红不绿 --------------------------------------
export GOOD_N=1
mkdir -p "$tmp/arec"
: > "$tmp/state"
export STATE="$tmp/state"
SEEK_EVAL_RESULTS_DIR="$tmp/arec" bash "$sb/eval/run.sh" --rounds 3 "$REC" "$fake" > "$tmp/arec.out" 2>/dev/null
rc=$?
[ "$rc" -eq 0 ] || fail "④ record-only run exited ${rc} (want 0 — never gated)"
grep -q '^RECORD  zrounds-record  (1/3 rounds, record-only — not gated)$' "$tmp/arec.out" \
  || fail "④ missing/incorrect RECORD line: $(grep RECORD "$tmp/arec.out")"
grep -q '^eval: 0 passed, 0 failed, 1 recorded (record-only)$' "$tmp/arec.out" \
  || fail "④ summary must split recorded column: $(grep 'eval:' "$tmp/arec.out" | tail -1)"
jq -e -s 'any(.[]; .type == "aggregate" and .verdict == "record" and .record_only == true and .passed == 1 and .rounds == 3)' "$tmp/arec/"*.jsonl >/dev/null \
  || fail "④ record-only aggregate row wrong: $(jq -c 'select(.type == "aggregate")' "$tmp/arec/"*.jsonl)"

# ---- ⑤：畸形输入启动即 exit 2、零行写入 -----------------------------------
malformed_rc2() { # malformed_rc2 LABEL EXPECT_JSON ARGS...
  local label=$1 expect=$2; shift 2
  local d="$tmp/mal-$label"
  mkdir -p "$d"
  printf '%s\n' "$expect" > "$sb/eval/cases/$CASE/expect.json"
  : > "$tmp/state"
  export STATE="$tmp/state"
  SEEK_EVAL_RESULTS_DIR="$d" bash "$sb/eval/run.sh" "$@" "$CASE" "$fake" > "$tmp/mal-$label.out" 2>/dev/null
  local rc=$?
  [ "$rc" -eq 2 ] || { fail "⑤ ${label}: want exit 2, got ${rc}"; return; }
  local rows
  rows=$(cat "$d/"*.jsonl 2>/dev/null | wc -l | tr -d ' ')
  [ "$rows" = "0" ] || fail "⑤ ${label}: ${rows} rows written before startup rejection"
}
malformed_rc2 rounds-zero      '{"max_turns":3}'                          --rounds 0
malformed_rc2 rounds-nonnum    '{"max_turns":3}'                          --rounds abc
malformed_rc2 rounds-eq-nonnum '{"max_turns":3}'                          --rounds=2x
malformed_rc2 min-above-one    '{"max_turns":3,"min_pass_rate":1.5}'      --rounds 2
malformed_rc2 min-negative     '{"max_turns":3,"min_pass_rate":-0.5}'     --rounds 2
malformed_rc2 min-zero         '{"max_turns":3,"min_pass_rate":0}'        --rounds 2
malformed_rc2 min-string       '{"max_turns":3,"min_pass_rate":"0.6"}'    --rounds 2
malformed_rc2 ro-string        '{"max_turns":3,"record_only":"yes"}'      --rounds 2
# 无效 JSON 整体也应 exit 2（头部契约：unparseable expect.json）。
malformed_rc2 json-broken      '{"max_turns":3,'                           --rounds 2

# ---- ⑥：trend reader 兼容——聚合行不双计 -----------------------------------
mkdir -p "$tmp/trendres"
: > "$tmp/trendres/2026-11-20-v9.9.9-roundsbeef.jsonl"
for k in 1 2 3; do
  printf '{"case":"%s","pass":true,"round":%d,"ran_at":"2026-11-19T10:0%d:00Z"}\n' "$CASE" "$k" "$k" >> "$tmp/trendres/2026-11-20-v9.9.9-roundsbeef.jsonl"
done
for k in 4 5; do
  printf '{"case":"%s","pass":false,"round":%d,"ran_at":"2026-11-19T10:0%d:00Z"}\n' "$CASE" "$k" "$k" >> "$tmp/trendres/2026-11-20-v9.9.9-roundsbeef.jsonl"
done
printf '{"case":"%s","type":"aggregate","rounds":5,"passed":3,"pass_rate":0.6,"min_pass_rate":0.6,"record_only":false,"verdict":"pass","ran_at":"2026-11-19T10:05:00Z"}\n' "$CASE" >> "$tmp/trendres/2026-11-20-v9.9.9-roundsbeef.jsonl"
TREND_RESULTS="$tmp/trendres" \
TREND_CASES="$sb/eval/cases" \
TREND_OUT_MD="$tmp/trend.md" TREND_OUT_JSON="$tmp/trend.json" \
TREND_NOW="2026-11-20T00:00:00Z" \
  bash scripts/eval-trend.sh > "$tmp/trend.out" 2>&1 || fail "⑥ eval-trend.sh failed on round-bearing rows: $(tail -2 "$tmp/trend.out")"
runs=$(jq -r '.cases[] | select(.case == "'"$CASE"'") | .runs' "$tmp/trend.json")
[ "$runs" = "5" ] || fail "⑥ trend must count 5 sample rows (aggregate row double-counted?): got ${runs}"
skipped=$(jq -r '.rows_skipped' "$tmp/trend.json")
[ "$skipped" = "1" ] || fail "⑥ trend must skip exactly the 1 aggregate row, got ${skipped}"

# ---- 收尾：真实 eval/results 零污染 ----------------------------------------
results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] || fail "eval/results inventory changed: $(diff <(printf '%s\n' "$results_before") <(printf '%s\n' "$results_after") | head -5)"

if [ "$fails" -eq 0 ]; then
  echo "eval-rounds selftest: PASS (rounds-1 byte-equivalence + 5-round aggregation w/ crash denominator + boundary green/red + record-only + malformed exit-2 + trend no-double-count)"
  exit 0
fi
echo "eval-rounds selftest: FAIL (${fails} assertion(s))" >&2
exit 1
