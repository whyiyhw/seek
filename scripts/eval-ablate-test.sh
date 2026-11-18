#!/bin/bash
# eval-ablate-test.sh — eval/ablate.sh 与 run.sh env 钩子的确定性自测
# （c_w3vhcm 返工增补①，2026-11-18；脚本自动进 selftest 发现名单，
#  裸跑即自测，无需 dispatch 参数）。
#
# 为什么存在：原卡验收①②靠沙盒里临时手跑假二进制，被验证车道权限层
# 拒跑、事后无法机械复跑（主会话判「验证存疑」退返工）。本脚本把同一套
# 验证收进受审代码：用打印固定 JSONL 的假二进制在车道内复验——
#
#   ① 双跑与对照表：假二进制按命令行是否出现 -tools 切换两套固定事件
#      流（full=1 tool_call / bare=2），驱动 eval/ablate.sh 单用例双跑，
#      断言 exit 0、eval/ablation/{full,bare}/ 各落 JSONL、对照表 .md
#      生成且本用例行的 tool_calls 单元格为 1/2 —— F/B 差异证明
#      SEEK_EVAL_EXTRA_ARGS 真的送达了 seek 调用（车道转向没有断链）；
#   ② eval/results/ 零污染：find 全量清单（含子目录）跑前跑后逐字节相等；
#   ③ 无环境变量时 run.sh 输出与「改动前」字节一致：改动前版本从 git
#      历史机械提取（eval/run.sh 历史里最新一个不含 SEEK_EVAL_RESULTS_DIR
#      的版本），与当前版本在两个临时沙盒各跑一遍假二进制，stdout+stderr+
#      结果 JSONL 行归一化后 cmp。归一化只动「同一脚本连跑两次也会变」的
#      字段：ran_at、elapsed_s、结果文件名里的日期戳、沙盒 tmp 路径——
#      不动任何行为语义。
#
# 假二进制与合成用例只写进 mktemp 沙盒与 eval/cases/<临时名>/，退场时
# 全部清掉；本测试落在 eval/ablation/ 的文件按 mtime 晚于测试起点识别并
# 删除——预先存在的真实消融数据（若有）分毫不动。
#
# golden 的时效性：③ 把「当前 run.sh 无 env 行为 == 最后一个改动前版本
# 的行为」钉成不变量。未来卡片若有意改 run.sh 的无 env 输出，本测试转
# 红——届时随那张卡更新基准（这正是验收②要守的东西）。
#
# macOS bash 3.2 兼容（selftest 通道全仓口径）：无 mapfile / 关联数组；
# ${VAR} 后紧跟非 ASCII 字符一律加花括号；默认值展开一律 :- 形态。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-ablate selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-ablate selftest: jq required" >&2; exit 2; }
command -v git >/dev/null 2>&1 || { echo "eval-ablate selftest: git required" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "eval-ablate selftest: not a git repo" >&2; exit 2; }

# z 前缀远离真实用例域；trap 保证异常退出也清场。
CASE=zablate-selftest
tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-ablate-selftest.XXXXXX")
# 规范化（cd+pwd）：macOS 的 TMPDIR 带尾斜杠 → mktemp 返回串含 //，且
# /var 是 /private/var 的符号链接；而 run.sh 打印的路径都过了一遍
# cd+pwd（物理路径、折叠重复分隔符）。两侧字符串不一致时 sed 归一化
# 静默不命中（首版实测踩中：只归一化了日期、路径裸奔进 diff）。
tmp=$(cd "$tmp" && pwd) || exit 2
marker="$tmp/marker"
: > "$marker"

cleanup() {
  # 顺序载入语义：先按 marker（在 $tmp 里）识别并删本测试新落的
  # ablation 文件，再 rm -rf "$tmp"——反过来 marker 先没了，find 静默
  # 空转（首版实测踩中：残留 eval/ablation/ 未清）。
  if [ -d eval/ablation ] && [ -f "$marker" ]; then
    find eval/ablation -type f -newer "$marker" -exec rm -f {} + 2>/dev/null
  fi
  rm -rf "eval/cases/$CASE" "$tmp"
  # 空目录撤掉（本测试新建的）；非空 = 有预先存在的数据，留着。
  rmdir eval/ablation/full eval/ablation/bare eval/ablation 2>/dev/null
  return 0
}
trap cleanup EXIT

# ---- 假二进制：-version 应答固定版本串；其余调用按 -tools 切流 ---------
# 版本串决定结果文件名（v9.9.9 · deadbee），全链路确定性。
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/bin/bash
# fake seek binary — prints a fixed JSONL event stream (c_w3vhcm selftest)
set -u
if [ "${1:-}" = "-version" ]; then
  echo "v9.9.9 · deadbee"
  exit 0
fi
tools=0
for a in "$@"; do
  [ "$a" = "-tools" ] && tools=1
done
if [ "$tools" = 1 ]; then
  cat <<'EOF'
{"type":"tool_start","name":"read","args":"{}"}
{"type":"tool_end","name":"read","result":"ok"}
{"type":"tool_start","name":"edit","args":"{}"}
{"type":"tool_end","name":"edit","result":"ok"}
{"type":"text_delta","delta":"bare lane answer","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":800,"completion_tokens":40,"cache_hit_tokens":300,"session_id":"fake-s1"}
EOF
else
  cat <<'EOF'
{"type":"tool_start","name":"read","args":"{}"}
{"type":"tool_end","name":"read","result":"ok"}
{"type":"text_delta","delta":"full lane answer","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","prompt_tokens":1000,"completion_tokens":50,"cache_hit_tokens":400,"session_id":"fake-s1"}
EOF
fi
exit 0
OUTER
chmod +x "$fake"

# ---- 合成用例：无 setup/teardown/runner（零副作用），界全过两车道 ------
mkdir -p "eval/cases/$CASE"
printf 'what is the answer?\n' > "eval/cases/$CASE/prompt.txt"
# turns=1 两变体都满足 min；agent_ends=1 满足 exact → 两车道都 PASS，
# run.sh 双跑均 exit 0（FAIL 也本是合法数据，但 PASS 让断言面更窄）。
printf '{"max_turns":3,"min_turns":1,"exact_agent_ends":1}\n' > "eval/cases/$CASE/expect.json"

results_before=$(find eval/results 2>/dev/null | sort)

# ---- 验收①：ablate.sh 双跑 + 对照表 ------------------------------------
bash eval/ablate.sh "$CASE" "$fake" > "$tmp/ablate.out" 2> "$tmp/ablate.err"
ablate_rc=$?
if [ "$ablate_rc" -ne 0 ]; then
  fail "ablate.sh exited ${ablate_rc} (want 0)"
  sed 's/^/    /' "$tmp/ablate.err" >&2
fi
ls eval/ablation/full/*.jsonl >/dev/null 2>&1 || fail "no full-lane JSONL under eval/ablation/full/"
ls eval/ablation/bare/*.jsonl >/dev/null 2>&1 || fail "no bare-lane JSONL under eval/ablation/bare/"
table_file=$(ls -t eval/ablation/*.md 2>/dev/null | head -1)
if [ -z "$table_file" ]; then
  fail "no comparison table .md under eval/ablation/"
else
  grep -q '^| case |' "$table_file" || fail "table missing header row"
  grep -q '^| TOTAL |' "$table_file" || fail "table missing TOTAL row"
  # 只在本用例行内断言 F/B 单元格——预存的他用例行不背书。
  row=$(grep "| ${CASE} |" "$table_file" || true)
  if [ -z "$row" ]; then
    fail "table missing row for ${CASE}"
  else
    # turns F/B=1/1、tool_calls F/B=1/2：差异单元格证明 -tools 真送达。
    case "$row" in
      *'| 1/1 | 1/2 |'*) ;;
      *) fail "case row F/B cells wrong (want turns 1/1, tool_calls 1/2 — lane steering broken?): $row" ;;
    esac
    case "$row" in
      *'| 1000/800 |'*) ;;
      *) fail "case row prompt-token F/B wrong (want 1000/800): $row" ;;
    esac
  fi
fi

# ---- 验收②（前半）：eval/results/ 零新增 --------------------------------
results_after=$(find eval/results 2>/dev/null | sort)
if [ "$results_before" != "$results_after" ]; then
  fail "eval/results inventory changed (ablation rows must never land there)"
  diff <(printf '%s\n' "$results_before") <(printf '%s\n' "$results_after") \
    | sed 's/^/    /' >&2
fi

# ---- 验收②（后半）：无 env 时 run.sh 输出与改动前字节一致 ----------------
# 改动前 = eval/run.sh 历史中最新一个不含 SEEK_EVAL_RESULTS_DIR 的 blob
# （env 钩子由 c_w3vhcm 引入）。找不到说明历史被改写/浅克隆——报错退出，
# 不静默跳过。
pre=""
for c in $(git log --format=%H -- eval/run.sh); do
  if ! git show "$c":eval/run.sh 2>/dev/null | grep -q SEEK_EVAL_RESULTS_DIR; then
    pre=$c
    break
  fi
done
if [ -z "$pre" ]; then
  echo "eval-ablate selftest: cannot locate pre-change run.sh in git history" >&2
  exit 2
fi

mkdir -p "$tmp/old/eval/cases/$CASE" "$tmp/new/eval/cases/$CASE"
git show "$pre":eval/run.sh > "$tmp/old/eval/run.sh"
cp eval/run.sh "$tmp/new/eval/run.sh"
for side in old new; do
  cp "eval/cases/$CASE/prompt.txt" "eval/cases/$CASE/expect.json" "$tmp/$side/eval/cases/$CASE/"
done

# env -u：车道环境里即使残留这两个变量也不影响「不设」口径。
env -u SEEK_EVAL_RESULTS_DIR -u SEEK_EVAL_EXTRA_ARGS \
  bash "$tmp/old/eval/run.sh" "$CASE" "$fake" > "$tmp/old.out" 2> "$tmp/old.err"
old_rc=$?
env -u SEEK_EVAL_RESULTS_DIR -u SEEK_EVAL_EXTRA_ARGS \
  bash "$tmp/new/eval/run.sh" "$CASE" "$fake" > "$tmp/new.out" 2> "$tmp/new.err"
new_rc=$?
[ "$old_rc" -eq 0 ] || fail "pre-change run.sh exited ${old_rc} (want 0)"
[ "$new_rc" -eq 0 ] || fail "current run.sh exited ${new_rc} (want 0)"

# 归一化：沙盒根（old/new 各自归 ROOT）、tmp 公共前缀、ran_at 时间戳、
# 结果文件名日期戳、PASS 行耗时、行内 elapsed_s —— 全部是同一脚本连跑
# 两次也会变的字段；其余字节必须一致。
norm() {
  sed -E \
    -e "s|$tmp/$1|ROOT|g" \
    -e "s|$tmp|TMP|g" \
    -e 's|20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z|TS|g' \
    -e 's|20[0-9]{2}-[0-9]{2}-[0-9]{2}-v9\.9\.9-deadbee|DATE-v9.9.9-deadbee|g' \
    -e 's|\([0-9]+s,|(Xs,|g' \
    -e 's|elapsed_s":[0-9]+|elapsed_s":E|g'
}
for side in old new; do
  set -- "$tmp/$side/eval/results/"*.jsonl
  if [ ! -f "$1" ]; then
    fail "$side sandbox wrote no results row"
    : > "$tmp/$side.rows"
  else
    cat "$@" > "$tmp/$side.rows"
  fi
  cat "$tmp/$side.out" "$tmp/$side.err" "$tmp/$side.rows" | norm "$side" > "$tmp/$side.norm"
  # 空产物防御：sed 表达式出错时两侧同样归一化为空，cmp 两个空文件会
  # 假绿（首版实测踩中：缺 | 分隔符 → "unescaped newline" → 双空 →
  # PASS）。非空断言把这条退化路径钉死。
  [ -s "$tmp/$side.norm" ] || fail "$side normalized output is empty (norm/sed broken?)"
done
if ! cmp -s "$tmp/old.norm" "$tmp/new.norm"; then
  fail "no-env run.sh output differs from pre-change ${pre}:"
  diff "$tmp/old.norm" "$tmp/new.norm" | head -10 | sed 's/^/    /' >&2
fi

if [ "$fails" -eq 0 ]; then
  echo "eval-ablate selftest: PASS (dual-run table + eval/results untouched + no-env run.sh byte-identical to ${pre})"
  exit 0
fi
echo "eval-ablate selftest: FAIL (${fails} assertion(s))" >&2
exit 1
