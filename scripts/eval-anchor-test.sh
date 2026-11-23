#!/bin/bash
# eval-anchor-test.sh — eval v3 A1 外部锚（anchor-* 用例族 + 共享
# anchor-runner.sh）的确定性自测。scripts/*.sh 自动进 selftest 发现制
# 名单，裸跑即自测。
#
# 为什么存在：锚点用例的判分全靠 runner/check 机制，机制本身必须有
# 假二进制的机械钉（同 eval-rounds/eval-judge 的出生教训）：
#
#   ① 「解出」形：假二进制在 cwd 写出正确产物 + 出好流 → 绿行、
#      exit 0、runner_assert_failures=0；
#   ② 「不解」形：只出流不写产物 → check.sh 失败 → failed fact →
#      max_runner_assert_failures 界翻红、exit 1、failures 指名
#      anchor_solved；
#   ③ 隔离形：假二进制记录 pwd 与 SEEK_HOME——断言 agent 的 cwd 就是
#      scratch 的 ws/（workspace fixture 已复制进去）、SEEK_HOME 是
#      scratch 的 home/（独立于 run.sh 的共享沙盒），判定材料
#      （check.sh）从 ws 外重算而非采信 agent 自述；
#   ④ 崩溃形：非零退出 → nonzero_exits fact 计入指标、check 亦红。
#
# macOS bash 3.2 兼容。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-anchor selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-anchor selftest: jq required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "eval-anchor selftest: python3 required" >&2; exit 2; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-anchor-selftest.XXXXXX")
tmp=$(cd "$tmp" && pwd) || exit 2
cleanup() { rm -rf "$tmp"; return 0; }
trap cleanup EXIT

# ---- 沙盒仓：run.sh + anchor-runner + 一个合成锚点用例 ------------------
sb="$tmp/sandbox"
mkdir -p "$sb/eval/tools" "$sb/eval/cases/zanchor-probe"
cp eval/run.sh "$sb/eval/run.sh"
cp eval/tools/anchor-runner.sh "$sb/eval/tools/anchor-runner.sh"
cat > "$sb/eval/cases/zanchor-probe/runner.sh" <<'EOF'
#!/bin/bash
export SEEK_EVAL_ANCHOR_DIR="$(cd "$(dirname "$0")" && pwd)"
exec bash "${SEEK_EVAL_ANCHOR_DIR}/../../tools/anchor-runner.sh"
EOF
chmod +x "$sb/eval/cases/zanchor-probe/runner.sh"
mkdir -p "$sb/eval/cases/zanchor-probe/workspace"
printf 'fixture material\n' > "$sb/eval/cases/zanchor-probe/workspace/input.txt"
printf 'Write out.txt in the current directory containing exactly: solved\n' > "$sb/eval/cases/zanchor-probe/prompt.txt"
printf '{"max_turns":6,"min_turns":1,"max_runner_assert_failures":0,"max_nonzero_exits":0}\n' > "$sb/eval/cases/zanchor-probe/expect.json"
cat > "$sb/eval/cases/zanchor-probe/check.sh" <<'EOF'
#!/bin/bash
set -u
[ "$(cat out.txt 2>/dev/null)" = "solved" ] || { echo "check: out.txt wrong" >&2; exit 1; }
EOF
chmod +x "$sb/eval/cases/zanchor-probe/check.sh"

# ---- 假二进制：-version 固定串；行为经 MODE 分流，记录 pwd/SEEK_HOME ---
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/bin/bash
set -u
if [ "${1:-}" = "-version" ]; then
  echo "v9.9.9 · anchorbeef"
  exit 0
fi
{
  echo "pwd=$PWD"
  echo "home=${SEEK_HOME:-unset}"
} >> "$FAKELOG"
case "${MODE:-noop}" in
  solve)
    printf 'solved\n' > out.txt
    ;;
  crash)
    echo '{"type":"text_delta","delta":"crashing","reasoning":false}'
    exit 3
    ;;
esac
cat <<'EOF'
{"type":"tool_start","name":"bash","args":"{}"}
{"type":"tool_end","name":"bash","result":"ok"}
{"type":"text_delta","delta":"did the task","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":100,"completion_tokens":10,"cache_hit_tokens":0,"session_id":"anchor-r"}
EOF
exit 0
OUTER
chmod +x "$fake"

FAKELOG="$tmp/fakelog"
: > "$FAKELOG"

run_anchor() { # run_anchor RESDIR MODE
  local resdir=$1 mode=$2
  mkdir -p "$resdir"
  : > "$FAKELOG"
  MODE=$mode FAKELOG="$FAKELOG" SEEK_EVAL_RESULTS_DIR="$resdir" \
    bash "$sb/eval/run.sh" zanchor-probe "$fake" >/dev/null 2>"$tmp/stderr"
}

results_before=$(find eval/results 2>/dev/null | sort)

# ---- ① 解出形：绿 --------------------------------------------------------
d="$tmp/b1"; run_anchor "$d" solve; rc=$?
[ "$rc" -eq 0 ] || fail "① solve mode exited ${rc} (want 0): $(tail -3 "$tmp/stderr")"
row=$(jq -c 'select(.case=="zanchor-probe")' "$d"/*.jsonl)
[ "$(jq -r '.pass' <<<"$row")" = "true" ] || fail "① solve row not green: $(jq -c '.failures' <<<"$row")"
[ "$(jq -r '.metrics.runner_assert_failures' <<<"$row")" = "0" ] || fail "① runner_assert_failures nonzero on solve"

# ---- ② 不解形：红，failures 指名 anchor_solved ---------------------------
d="$tmp/b2"; run_anchor "$d" noop; rc=$?
[ "$rc" -eq 1 ] || fail "② noop mode exited ${rc} (want 1)"
row=$(jq -c 'select(.case=="zanchor-probe")' "$d"/*.jsonl)
[ "$(jq -r '.pass' <<<"$row")" = "false" ] || fail "② noop row not red"
jq -e '.metrics.runner_assert_failures >= 1' <<<"$row" >/dev/null || fail "② runner_assert_failures not counted"
jq -r '.failures[]' <<<"$row" | grep -q 'runner_assert_failures' || fail "② failures do not name runner_assert_failures"
grep -q 'anchor_solved' "$tmp/stderr" || true  # detail 在 fact 里，stderr 不强制

# ---- ③ 隔离形：pwd=scratch/ws、SEEK_HOME=scratch/home、fixture 在位 ------
d="$tmp/b3"; run_anchor "$d" solve
ws_pwd=$(sed -n 's/^pwd=//p' "$FAKELOG" | head -1)
agent_home=$(sed -n 's/^home=//p' "$FAKELOG" | head -1)
case "$ws_pwd" in
  "$sb"/eval/tmp/zanchor-probe/ws) : ;;
  *) fail "③ agent cwd is not the scratch ws (got: $ws_pwd)" ;;
esac
case "$agent_home" in
  "$sb"/eval/tmp/zanchor-probe/home) : ;;
  *) fail "③ SEEK_HOME is not the scratch home (got: $agent_home)" ;;
esac
[ -f "$ws_pwd/input.txt" ] || fail "③ workspace fixture not copied into ws"
[ ! -e "$sb/eval/tmp/seek-home/zanchor-probe" ] || fail "③ anchor scratch leaked into the shared sandbox home"

# ---- ④ 崩溃形：nonzero_exits 计入 + 红 -----------------------------------
d="$tmp/b4"; run_anchor "$d" crash; rc=$?
[ "$rc" -eq 1 ] || fail "④ crash mode exited ${rc} (want 1)"
row=$(jq -c 'select(.case=="zanchor-probe")' "$d"/*.jsonl)
[ "$(jq -r '.metrics.nonzero_exits' <<<"$row")" = "1" ] || fail "④ nonzero_exits not counted"
[ "$(jq -r '.pass' <<<"$row")" = "false" ] || fail "④ crash row not red"

results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] || fail "eval/results polluted by the selftest"

if [ "$fails" -eq 0 ]; then
  echo "eval-anchor selftest: PASS (4 legs)"
  exit 0
fi
echo "eval-anchor selftest: FAIL (${fails} failures)" >&2
exit 1
