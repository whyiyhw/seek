#!/bin/bash
# eval-lane-test.sh — eval/run.sh --lane 名单机制的确定性自测（eval v2 E6，
# c_rkoeiy，2026-11-21；scripts/*.sh 自动进 selftest 发现制名单，裸跑即
# 自测，零登记）。
#
# 为什么存在：同 eval-rounds-test.sh 的出生事故教训——沙盒手跑的验收
# 事后无法机械复跑，验收必须收进受审代码。本脚本用固定事件流的假二进
# 制钉死：
#
#   ① 无 --lane 时输出与 E6 改动前的 run.sh 字节等价：git 历史里最新
#      一个 eval/run.sh 不含 --lane 的 blob（归一化后 cmp stdout+
#      stderr+结果行；归一化只动同一脚本连跑两次也会变的字段）。
#      与 eval-rounds-test 的「--rounds 1 ≡ 无 flag」、eval-ablate-test
#      的「无 env ≡ c_w3vhcm 前 blob」同族——三测合起来：无 flag ≡ 历史。
#      例外：metric vocabulary 的 append-only 扩展与 ablate 自测同款白名
#      单（norm 剥新键默认 0 值，值非 0 不剥、diff 照样红——守护面不弱
#      化）。2026-11-21 起清单：runner_assert_failures（E3 plan-e2e，
#      c_c5wwey；本测出生时锚点 blob 取自 main 系 E5 版，E3 并回后基线
#      对象带上该键，需同规则吸收）。2026-11-21 第二批：references_calls /
#      bash_grep_calls / bash_cd_prefix_calls / parallel_turns（40 会话
#      基线指标，ablate 同批同款）。2026-11-22 第一批**删除**：think_calls
#      （think 工具退役，c_cs4dw6）——锚侧 blob 带该键、现侧不带，同规则
#      剥默认 0 值。
#   ② --lane 只跑名单内用例：判定行与结果行的用例集合 == 名单、顺序 ==
#      名单顺序（含 # 整行注释、行尾注释、缩进、空行的解析）；名单外
#      用例零出现；stdout 有 lane 头行。
#   ③ --lane 与显式用例名互斥 → exit 2 零行写入；
#   ④ 名单文件不存在 → exit 2；
#   ⑤ 名单含未知用例 → 启动 exit 2 零行写入（名单是策略，拼错必须响亮
#      失败，不许静默跳过——E1「不许静默降级」同款纪律）；
#   ⑥ 名单全注释/空 → exit 2；
#   ⑦ --lane 名含 [a-z0-9-] 之外字符（也是路径安全闸：'/' 与 '.' 都在
#      字符集外，逃不出 eval/lane-*.txt）→ exit 2；
#   ⑧ 仓内两份真名单（lane-release.txt / lane-core.txt）逐条对得上
#      eval/cases/<name>/ 目录，且承重轴 serve-web-stream 两条道都在；
#   ⑨ release 名单里不含任何 record_only 用例（c_3xyt7w 定案：名单每行都是
#      闸门，record_only 的行永不染色 —— 写进闸门只会让它永久绿、看起来在
#      守。同族 pitfall: docs/pitfalls/eval/record-only-cases-hide-their-
#      verdict-in-the-per-round-line-*.md）。反向腿在沙盒里改名单验证它会红。
#
# macOS bash 3.2 兼容（selftest 通道全仓口径）：无 mapfile / 关联数组。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-lane selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-lane selftest: jq required" >&2; exit 2; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-lane-selftest.XXXXXX")
# 规范化（cd+pwd）：macOS TMPDIR 带尾斜杠 / /var 符号链接，与 run.sh
# 打印的 cd+pwd 物理路径对齐，否则 sed 归一化静默不命中（同 rounds 自测）。
tmp=$(cd "$tmp" && pwd) || exit 2

cleanup() {
  rm -rf "$tmp"
  return 0
}
trap cleanup EXIT

CASE_A=zlane-aaa
CASE_B=zlane-bbb
CASE_C=zlane-ccc

# ---- 假二进制：-version 固定版本串；事件流恒好（过 exact_agent_ends 界）
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/bin/bash
set -u
if [ "${1:-}" = "-version" ]; then
  echo "v9.9.9 · lanecafe"
  exit 0
fi
cat <<'EOF'
{"type":"tool_start","name":"read","args":"{}"}
{"type":"tool_end","name":"read","result":"ok"}
{"type":"text_delta","delta":"lane answer","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":800,"completion_tokens":30,"cache_hit_tokens":200,"session_id":"fake-l"}
EOF
exit 0
OUTER
chmod +x "$fake"

mkcase() { # mkcase CASE_DIR
  mkdir -p "$1"
  printf 'what is the answer?\n' > "$1/prompt.txt"
  printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1}\n' > "$1/expect.json"
}

results_before=$(find eval/results 2>/dev/null | sort)

# ---- ①：无 --lane ≡ E6 改动前（git 历史 blob 对照） ------------------------
pre=""
for c in $(git log --format=%H -- eval/run.sh); do
  if ! git show "$c":eval/run.sh 2>/dev/null | grep -q -- '--lane'; then
    pre=$c
    break
  fi
done
if [ -z "$pre" ]; then
  echo "eval-lane selftest: cannot locate pre-E6 run.sh in git history" >&2
  exit 2
fi
mkdir -p "$tmp/old/eval/cases/$CASE_A" "$tmp/new/eval/cases/$CASE_A"
git show "$pre":eval/run.sh > "$tmp/old/eval/run.sh"
cp eval/run.sh "$tmp/new/eval/run.sh"
mkcase "$tmp/old/eval/cases/$CASE_A"
mkcase "$tmp/new/eval/cases/$CASE_A"
for side in old new; do
  env -u SEEK_EVAL_RESULTS_DIR -u SEEK_EVAL_EXTRA_ARGS \
    -u SEEK_EVAL_PROVIDER -u SEEK_EVAL_MODEL -u SEEK_EVAL_BASE_URL -u SEEK_EVAL_API_KEY \
    bash "$tmp/$side/eval/run.sh" "$CASE_A" "$fake" \
    > "$tmp/$side.out" 2> "$tmp/$side.err" \
    || fail "① $side run.sh exited non-zero (want 0)"
done
norm() { # norm SUBDIR —— 只动「同一脚本连跑两次也会变」的字段
  sed -E \
    -e "s|$tmp/$1|ROOT|g" \
    -e "s|$tmp|TMP|g" \
    -e 's|20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z|TS|g' \
    -e 's|20[0-9]{2}-[0-9]{2}-[0-9]{2}-v9\.9\.9-lanecafe|DATEFILE|g' \
    -e 's|\([0-9]+s,|(Xs,|g' \
    -e 's|elapsed_s":[0-9]+|elapsed_s":E|g' \
    -e 's|,"think_calls":0||g' \
    -e 's|,"runner_assert_failures":0||g' \
    -e 's|,"references_calls":0||g' \
    -e 's|,"bash_grep_calls":0||g' \
    -e 's|,"bash_cd_prefix_calls":0||g' \
    -e 's|,"parallel_turns":0||g'
}
for side in old new; do
  set -- "$tmp/$side/eval/results/"*.jsonl
  if [ ! -f "$1" ]; then
    fail "① $side sandbox wrote no results row"
    : > "$tmp/$side.rows"
  else
    cat "$@" > "$tmp/$side.rows"
  fi
  cat "$tmp/$side.out" "$tmp/$side.err" "$tmp/$side.rows" | norm "$side" > "$tmp/$side.norm"
  # 空产物防御：sed 出错两侧同空 → cmp 假绿（ablate 自测同款教训）。
  [ -s "$tmp/$side.norm" ] || fail "① $side normalized output empty (norm/sed broken?)"
done
cmp -s "$tmp/old.norm" "$tmp/new.norm" \
  || fail "① no-flag run.sh output differs from pre-E6 ${pre}: $(diff "$tmp/old.norm" "$tmp/new.norm" | head -5)"

# ---- ②：名单过滤生效（集合 == 名单、顺序 == 名单顺序） ---------------------
sb="$tmp/sb"
mkdir -p "$sb/eval/cases"
cp eval/run.sh "$sb/eval/run.sh"
mkcase "$sb/eval/cases/$CASE_A"
mkcase "$sb/eval/cases/$CASE_B"
mkcase "$sb/eval/cases/$CASE_C"
{
  echo "# full-line comment"
  echo ""
  echo "$CASE_C"
  echo "  $CASE_A  # trailing comment"
} > "$sb/eval/lane-sub.txt"
mkdir -p "$tmp/lanerun"
env -u SEEK_EVAL_RESULTS_DIR -u SEEK_EVAL_EXTRA_ARGS \
  -u SEEK_EVAL_PROVIDER -u SEEK_EVAL_MODEL -u SEEK_EVAL_BASE_URL -u SEEK_EVAL_API_KEY \
  SEEK_EVAL_RESULTS_DIR="$tmp/lanerun" \
  bash "$sb/eval/run.sh" --lane sub '' "$fake" > "$tmp/lanerun.out" 2>"$tmp/lanerun.err"
rc=$?
[ "$rc" -eq 0 ] || fail "② --lane sub exited ${rc} (want 0): $(tail -2 "$tmp/lanerun.err")"
order=$(grep -oE '^(PASS|FAIL)  zlane-[a-z]+' "$tmp/lanerun.out" | awk '{print $2}')
want_order=$(printf '%s\n%s' "$CASE_C" "$CASE_A")
[ "$order" = "$want_order" ] \
  || fail "② lane run order/content wrong: got '$order' (want '$(printf '%s / %s' "$CASE_C" "$CASE_A")')"
grep -q -- "$CASE_B" "$tmp/lanerun.out" && fail "② non-listed case $CASE_B must not appear in stdout"
ran_cases=$(jq -r '.case' "$tmp/lanerun/"*.jsonl | sort -u)
want_cases=$(printf '%s\n%s\n' "$CASE_A" "$CASE_C" | sort)
[ "$ran_cases" = "$want_cases" ] || fail "② result rows name wrong case set: got '$ran_cases'"
grep -q '^eval: lane    sub (2 cases from lane-sub.txt)$' "$tmp/lanerun.out" \
  || fail "② missing/incorrect lane header line: $(grep '^eval: lane' "$tmp/lanerun.out")"

# ---- ③-⑦：畸形/冲突输入启动即 exit 2、零行写入 ----------------------------
# ⑤⑥ 的名单文件放沙盒 eval/ 里（run.sh 按 repo 相对路径读名单）。
printf '%s\n' "$CASE_A" 'no-such-case' > "$sb/eval/lane-bad.txt"
printf '# only comments\n\n' > "$sb/eval/lane-empty.txt"
malformed_rc2() { # malformed_rc2 LABEL ARGS...
  local label=$1; shift
  local d="$tmp/mal-$label"
  mkdir -p "$d"
  env -u SEEK_EVAL_RESULTS_DIR -u SEEK_EVAL_EXTRA_ARGS \
    -u SEEK_EVAL_PROVIDER -u SEEK_EVAL_MODEL -u SEEK_EVAL_BASE_URL -u SEEK_EVAL_API_KEY \
    SEEK_EVAL_RESULTS_DIR="$d" \
    bash "$sb/eval/run.sh" "$@" > "$tmp/mal-$label.out" 2>/dev/null
  local rc=$?
  [ "$rc" -eq 2 ] || { fail "③-⑦ ${label}: want exit 2, got ${rc}"; return; }
  local rows
  rows=$(cat "$d/"*.jsonl 2>/dev/null | wc -l | tr -d ' ')
  [ "$rows" = "0" ] || fail "③-⑦ ${label}: ${rows} rows written before startup rejection"
}
# NB：单测车道各臂一律带 '' 占位（空 case 名 + 二进制）——不带时 $fake
# 会被当成 case 过滤器，互斥臂替目标臂挡枪，测试假绿。
malformed_rc2 lane-plus-case   --lane sub "$CASE_A" "$fake"
malformed_rc2 lane-no-file     --lane nosuch '' "$fake"
malformed_rc2 lane-unknown-case --lane bad '' "$fake"
malformed_rc2 lane-empty-list  --lane empty '' "$fake"
malformed_rc2 lane-bad-charset --lane '../x' '' "$fake"
malformed_rc2 lane-eq-empty    --lane= '' "$fake"

# ---- ⑧：仓内真名单逐条可解析、逐条有用例目录 -------------------------------
for lf in eval/lane-release.txt eval/lane-core.txt; do
  if [ ! -f "$lf" ]; then
    fail "⑧ missing lane file $lf"
    continue
  fi
  n=0
  while read -r raw || [ -n "$raw" ]; do
    entry=${raw%%#*}
    entry=$(printf '%s' "$entry" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
    [ -z "$entry" ] && continue
    [ -d "eval/cases/$entry" ] || fail "⑧ $lf lists unknown case: $entry"
    n=$((n+1))
  done < "$lf"
  [ "$n" -gt 0 ] || fail "⑧ $lf has no active entries"
done
grep -q '^serve-web-stream$' eval/lane-release.txt \
  || fail "⑧ release lane must list the landed load-bearing axis serve-web-stream"
grep -q '^serve-web-stream$' eval/lane-core.txt \
  || fail "⑧ core lane must list serve-web-stream (zero-cost fake-key axis)"
# ⑨：release 名单里不许出现 record_only 用例（c_3xyt7w 定案）。名单每一行
# 都是闸门，而 record_only 的行永不染色 ⇒ 写进来只会让闸门永久绿、看起来
# 在守（docs/pitfalls/eval/record-only-cases-hide-their-verdict-in-the-
# per-round-line-… 同族）。反之 plan-e2e 的"不在册"也由此机械保证，不必
# 在注释里靠人记。core 是观测道，不做此限制。
record_only_in_lane() { # record_only_in_lane LANE_FILE CASES_ROOT
  while read -r raw || [ -n "$raw" ]; do
    entry=${raw%%#*}
    entry=$(printf '%s' "$entry" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
    [ -z "$entry" ] && continue
    ro=$(jq -r '.record_only // false' "$2/$entry/expect.json" 2>/dev/null)
    if [ "$ro" = "true" ]; then
      printf '%s ' "$entry"
    fi
  done < "$1"
}
disp_ro=$(record_only_in_lane eval/lane-release.txt eval/cases)
if [ -n "$disp_ro" ]; then
  fail "⑨ release lane lists record-only case(s): ${disp_ro% } — 闸门会对它们永久绿"
fi
# 反向腿（防静默空转）：沙盒名单里放一个 record_only 用例，上面同一个函数
# 必须能点出它的名字——否则 ⑨ 只是永远空过，名单被污染时也不会红。
rowsb="$tmp/ro-lane"; mkdir -p "$rowsb"
printf '%s\n' "$CASE_A" > "$rowsb/lane-release.txt"
mkcase "$rowsb/cases/$CASE_A"
# 把一个沙盒用例标成 record_only，再要求函数报出它
printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1,"record_only":true}\n' \
  > "$rowsb/cases/$CASE_A/expect.json"
hits=$(record_only_in_lane "$rowsb/lane-release.txt" "$rowsb/cases")
case "$hits" in
  *"$CASE_A"*) : ;;
  *) fail "⑨ reverse leg: record-only detector missed $CASE_A in a sandbox lane (got: '${hits}')" ;;
esac
# 正向腿（钉路径约定）：拿**真** cases 根 + 一个沙盒名单，写上真 record-only
# 的 plan-e2e —— 必须被点出。这条同时证明 ⑨ 扫的是 eval/cases/<name>/expect.json
# 这个真实路径（否则真名单被污染时它仍会空过）。
printf '%s\n' "plan-e2e" > "$rowsb/lane-release.txt"
hits=$(record_only_in_lane "$rowsb/lane-release.txt" eval/cases)
case "$hits" in
  *plan-e2e*) : ;;
  *) fail "⑨ forward leg: plan-e2e (真 record_only 用例) 未被点出（got: '${hits}'）" ;;
esac

# ---- 收尾：真实 eval/results 零污染 ----------------------------------------
results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] || fail "eval/results inventory changed: $(diff <(printf '%s\n' "$results_before") <(printf '%s\n' "$results_after") | head -5)"

if [ "$fails" -eq 0 ]; then
  echo "eval-lane selftest: PASS (no-flag byte-equivalence vs pre-E6 blob + lane filtering/order + mutual-exclusion/unknown-case/empty-list/charset exit-2 + real lane lists valid + record-only-free release lane (both legs))"
  exit 0
fi
echo "eval-lane selftest: FAIL (${fails} assertion(s))" >&2
exit 1
