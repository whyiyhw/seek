#!/bin/bash
# eval-compact-ab-test.sh — compact-ab 用例的确定性自测（card c_qjb5mq）。
#
# 为什么存在：这个用例的核心断言是**取反的**——bad 变体里「arm B 答不出
# 事实」才算绿。取反断言最容易写成恒绿，而恒绿的断言永远不会报警。
# 本脚本用同一条管线跑三段，把「它能红」也钉进受审代码：
#
#   ① good 变体：arm_a_keeps_facts / arm_b_keeps_facts /
#      arm_b_is_summary_pair 全绿 → 行 PASS、runner_assert_failures=0；
#   ② bad 变体：arm A 仍绿（全历史不丢东西），
#      arm_b_negative_loses_facts 绿（摘要没带事实 ⇒ 端点答 NO-FACTS）→ PASS；
#   ③ 破坏态：bad 变体 + ALWAYS_ANSWER=1（端点无条件答出事实）
#      → arm_b_negative_loses_facts 必须红、行必须 FAIL——
#      证明②的绿不是「断言写太松」。
#
# 三段都用**真二进制**（go build，~4s），因为本用例验的正是「真 seek 读到
# 的会话历史里有什么」：假二进制可以自己编历史，等于把被测对象换掉。
# 模型端点是本仓 scripted fixture（假 key，零 API 费、零网络外呼）。
#
# macOS bash 3.2：无关联数组。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-compact-ab selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-compact-ab selftest: jq required" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "eval-compact-ab selftest: curl required" >&2; exit 2; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-compact-ab.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

go build -o "$tmp/seek" ./cmd/seek \
  || { echo "eval-compact-ab selftest: go build failed" >&2; exit 2; }

# Lane hygiene: the case is self-contained (own fixture endpoint + fake
# key), so every inherited lane knob must be scrubbed or the compatible
# lane URL could be overridden underneath it.
run_leg() { # $1=leg name, $2=expect (pass|fail), $3 extra env
  local name=$1 want=$2 extra=$3
  local resdir="$tmp/results-$name"
  mkdir -p "$resdir"
  local out rc
  out=$(env -u SEEK_EVAL_MODEL -u SEEK_EVAL_BASE_URL -u SEEK_EVAL_API_KEY \
        -u SEEK_EVAL_PROVIDER $extra \
        SEEK_EVAL_RESULTS_DIR="$resdir" \
        bash eval/run.sh compact-ab "$tmp/seek" 2>&1)
  rc=$?

  # The case is record_only, so the verdict line is "RECORD" and the exit
  # code stays 0 by design — the assertion surface is the per-ROUND line
  # (run_one prints PASS/FAIL before the RECORD summary). Reading only the
  # last line would silently pass every leg.
  local round_line
  round_line=$(printf '%s\n' "$out" | grep -E '^(PASS|FAIL)  compact-ab' | tail -1)
  if [[ -z "$round_line" ]]; then
    fail "$name: no per-round PASS/FAIL line (rc=$rc); output: $(printf '%s' "$out" | tail -3)"
    return
  fi
  if [[ "$want" == pass ]]; then
    if ! printf '%s' "$round_line" | grep -q '^PASS'; then
      fail "$name: want PASS, got: $round_line"
    fi
  else
    if ! printf '%s' "$round_line" | grep -q '^FAIL'; then
      fail "$name: want FAIL (assertion must bite), got: $round_line"
    fi
    # The red must come from the NEGATIVE assertion, not from some unrelated
    # metric failing (a FAIL that happens to be red is no proof the
    # inversion bites). run.sh's failure line only names the metric
    # (runner_assert_failures), so run the case's runner directly and read
    # the fact line itself.
    local raw
    raw=$(env -u SEEK_EVAL_MODEL -u SEEK_EVAL_PROVIDER \
          SEEK_EVAL_BINARY="$tmp/seek" \
          SEEK_EVAL_REPO_ROOT="$PWD" \
          SEEK_EVAL_PROMPT_FILE="$PWD/eval/cases/compact-ab/prompt.txt" \
          SEEK_EVAL_MAX_TURNS=3 $extra \
          bash eval/cases/compact-ab/runner.sh 2>/dev/null)
    if ! printf '%s' "$raw" | grep -q '"assert": "arm_b_negative_loses_facts", "failed": true'; then
      fail "$name: runner did not report arm_b_negative_loses_facts as failed"
    fi
    if printf '%s' "$raw" | grep -q '"assert": "arm_a_keeps_facts", "failed": true'; then
      fail "$name: arm A must stay green even in the sabotage leg"
    fi
  fi

  # Both arms must have run in every leg: two agent_ends, zero non-zero
  # exits. The metrics line rides the PASS line only, so check the whole
  # leg's output.
  printf '%s' "$out" | grep -q '"agent_ends":2' \
    || fail "$name: expected agent_ends=2"
  printf '%s' "$out" | grep -q '"nonzero_exits":0' \
    || fail "$name: expected nonzero_exits=0"
  # record_only must stay uncoloured: exit 0 in all three legs, including
  # the sabotage leg whose round is red.
  if (( rc != 0 )); then
    fail "$name: record_only must exit 0, got $rc"
  fi
}

run_leg good pass ""
run_leg bad pass "COMPACT_AB_SUMMARY=bad"
run_leg sabotage fail "COMPACT_AB_SUMMARY=bad ALWAYS_ANSWER=1"

if (( fails > 0 )); then
  echo "eval-compact-ab selftest: $fails assertion(s) failed" >&2
  exit 1
fi
echo "eval-compact-ab selftest: PASS（good 绿 / bad 绿 / 破坏态按名转红，record_only 三段均 exit 0）"
exit 0
