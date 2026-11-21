#!/bin/bash
# eval-e4-case-test.sh — subagent-bg-resume 承重轴用例的确定性自测
# (eval v2 E4，c_vollxv，2026-11-21；scripts/*.sh 自动进 selftest 发现制
# 名单，裸跑即自测，零登记)。
#
# 为什么存在：同 eval-rounds-test.sh 的出生事故教训——承重轴用例的
# 验收若只靠沙盒手跑假二进制，事后无法机械复跑。本脚本把 E4 的验收
# 收进受审代码，用一个模拟 seek 全生命周期写面的假二进制钉死：
#
#   ① 全过：好流两调用链（fresh 存会话 → -resume 同会话）判绿
#      （exit 0），行内九个 agent/bg 指标字段逐个断言（含数据门控：
#      键存在且值正确）；
#   ② 防假红（验收②）：五种断言失败形态逐个判红（exit 1），且失败
#      行点名具体指标（want min=1, got 0 等）——事件形状与字段断了
#      哪条，红就落哪条；同时断言输出不含 "unknown metric"（门控键
#      在失败轮也存在——runner_fact 通道无条件发射）：
#        notified   = 缺 notified 事件 + 缺 <context> 送达（两指标同红）
#        resumed    = 续话 started 不带 resumed_from
#        artifact   = 续话 transcript.jsonl 落盘缺失
#        cancelled  = 后台子被进程退出取消（failed reason=canceled）
#        stream     = 模型面红：run 1 无 agent 工具调用
#   ③ record_only（验收③前半）：RECORD 行、exit 0、不染色；
#   ④ E1 rounds 口径兼容（验收③后半）：--rounds 3、前 2 轮好后 1 轮
#      坏（artifact 破坏）→ 2/3；对 0.6 聚合判绿（exit 0）、对 1.0
#      聚合判红（exit 1）；3 行轮次行 + 1 行聚合行，verdict 与
#      pass_rate 逐字段核。
#
# 假二进制契约：吃与真 seek 相同的 argv 形状（-version / [-provider
# …] -max-turns N -json -p PROMPT / -resume SID …），按有无 -resume 分
# 辨第 1/2 次调用，把 run.sh 隔离的 $SEEK_HOME 下的真写面全部落盘
# （projects/fakeproj/subagents.jsonl 事件源、sessions/<sid>.jsonl 会话
# 文、sessions/<sid>/subagents/<sub>/transcript.jsonl 产物）。FAKE_STATE
# 按第 1 次调用计数轮次；轮次号 > FAKE_GOOD_ROUNDS 时套用 FAKE_BREAK
# 的破坏形态。
#
# macOS bash 3.2 兼容：无 mapfile / 关联数组。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-e4-case selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-e4-case selftest: jq required" >&2; exit 2; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-e4-case.XXXXXX")
tmp=$(cd "$tmp" && pwd) || exit 2
cleanup() {
  rm -rf "$tmp"
  return 0
}
trap cleanup EXIT

results_before=$(find eval/results 2>/dev/null | sort)

CASE=subagent-bg-resume

# ---- 沙盒仓：run.sh 副本 + 真用例目录原样拷入（测的就是真件） ------
sb="$tmp/sandbox"
mkdir -p "$sb/eval/cases"
cp eval/run.sh "$sb/eval/run.sh"
cp -R "eval/cases/$CASE" "$sb/eval/cases/$CASE"

# ---- 假二进制 -----------------------------------------------------------
# 内层 heredoc 不加引号（EOF 而非 'EOF'）：JSON 行里的 $SUB1/$SESS1 在
# 假二进制运行期展开为其顶部定义的常量；JSON 内容无其它 $ / 反引号。
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/bin/bash
set -u
if [ "${1:-}" = "-version" ]; then
  echo "v9.9.9 · e4beef"
  exit 0
fi

resume_sid=""
args=("$@")
i=0
while [ $i -lt ${#args[@]} ]; do
  if [ "${args[$i]}" = "-resume" ]; then
    resume_sid="${args[$((i+1))]:-}"
  fi
  i=$((i+1))
done

H="${SEEK_HOME:?fake-seek needs SEEK_HOME}"
proj="$H/projects/fakeproj"
index="$proj/subagents.jsonl"
SESS1="20261121-120000-e4fake0000000001"
SUB1="20261121-120001-e4fake0000000002"
SUB2="20261121-120002-e4fake0000000003"
mkdir -p "$proj"

break=""
if [ -n "${FAKE_STATE:-}" ]; then
  n=$(cat "$FAKE_STATE" 2>/dev/null || echo 0)
  if [ -z "$resume_sid" ]; then
    n=$((n+1)); echo "$n" > "$FAKE_STATE"
  fi
  if [ "$n" -gt "${FAKE_GOOD_ROUNDS:-99}" ] && [ -n "${FAKE_BREAK:-}" ]; then
    break="$FAKE_BREAK"
  fi
fi

ts() { printf '2026-11-21T12:00:%02dZ\n' "$1"; }

if [ -z "$resume_sid" ]; then
  # ---- 第 1 次调用：fresh 存会话 ----
  # stream 破坏 = 模型面红（没调 agent 工具）：盘面也不该有 spawn
  # 痕迹——真 seek 里没有工具调用就没有 started 事件。
  if [ "$break" != "stream" ]; then
    {
      printf '{"event":"started","sub_sid":"%s","ts":"%s","parent_sid":"%s","type":"general-purpose","description":"trivial word task","background":true}\n' "$SUB1" "$(ts 1)" "$SESS1"
      if [ "$break" = "cancelled" ]; then
        printf '{"event":"failed","sub_sid":"%s","ts":"%s","reason":"canceled"}\n' "$SUB1" "$(ts 2)"
      else
        printf '{"event":"completed","sub_sid":"%s","ts":"%s","tokens":{"prompt":100,"completion":5,"cache_hit":0}}\n' "$SUB1" "$(ts 2)"
        mkdir -p "$proj/sessions/$SESS1/subagents/$SUB1"
        printf '{"schema_version":2,"model":"fake","subagent":true}\n{"role":"user","content":"Reply with exactly the single word marlowe. Do not use any tools."}\n{"role":"assistant","content":"marlowe"}\n' > "$proj/sessions/$SESS1/subagents/$SUB1/transcript.jsonl"
      fi
    } >> "$index"
  fi
  mkdir -p "$H/sessions"
  printf '{"schema_version":2,"model":"fake","created_at":"2026-11-21T12:00:00Z","turns":1}\n{"role":"user","content":"spawn the bg subagent and read go.mod"}\n{"role":"assistant","content":"spawned; module github.com/whyiyhw/seek"}\n' > "$H/sessions/$SESS1.jsonl"
  if [ "$break" = "stream" ]; then
    cat <<EOF
{"type":"agent_start","model":"fake"}
{"type":"tool_start","name":"read","args":"{\\"path\\":\\"go.mod\\"}"}
{"type":"tool_end","name":"read","result":"module github.com/whyiyhw/seek"}
{"type":"text_delta","delta":"read go.mod","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","session_id":"$SESS1","prompt_tokens":900,"completion_tokens":40,"cache_hit_tokens":100}
EOF
  else
    cat <<EOF
{"type":"agent_start","model":"fake"}
{"type":"tool_start","name":"agent","args":"{\\"description\\":\\"trivial word task\\",\\"prompt\\":\\"Reply with exactly the single word marlowe. Do not use any tools.\\",\\"run_in_background\\":true}"}
{"type":"tool_end","name":"agent","result":"[agent: background started sub-sid=$SUB1] trivial word task"}
{"type":"tool_start","name":"read","args":"{\\"path\\":\\"go.mod\\"}"}
{"type":"tool_end","name":"read","result":"module github.com/whyiyhw/seek"}
{"type":"text_delta","delta":"spawned; module github.com/whyiyhw/seek","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","session_id":"$SESS1","prompt_tokens":900,"completion_tokens":40,"cache_hit_tokens":100}
EOF
  fi
  exit 0
fi

# ---- 第 2 次调用：-resume 同会话 ----
if [ "$break" != "cancelled" ] && [ "$break" != "stream" ]; then
  if [ "$break" != "notified" ]; then
    printf '{"event":"notified","sub_sid":"%s","ts":"%s"}\n' "$SUB1" "$(ts 3)" >> "$index"
    printf '{"role":"user","content":"<context>\\n[agent: background done sub-sid=%s] marlowe\\n\\nmarlowe\\n\\n— sub-sid: e4fake0000000002 — delivered on session resume\\n</context>"}\n' "$SUB1" >> "$H/sessions/$SESS1.jsonl"
  else
    printf '{"role":"user","content":"<context>\\n[agent: background lost sub-sid=%s] status=orphaned\\n</context>"}\n' "$SUB1" >> "$H/sessions/$SESS1.jsonl"
  fi
  if [ "$break" = "resumed" ]; then
    printf '{"event":"started","sub_sid":"%s","ts":"%s","parent_sid":"%s","type":"general-purpose","description":"continue %s","background":false}\n' "$SUB2" "$(ts 4)" "$SESS1" "$SUB1" >> "$index"
  else
    printf '{"event":"started","sub_sid":"%s","ts":"%s","parent_sid":"%s","type":"general-purpose","description":"continue %s","resumed_from":"%s","background":false}\n' "$SUB2" "$(ts 4)" "$SESS1" "$SUB1" "$SUB1" >> "$index"
  fi
  printf '{"event":"completed","sub_sid":"%s","ts":"%s","tokens":{"prompt":120,"completion":3,"cache_hit":0}}\n' "$SUB2" "$(ts 5)" >> "$index"
  if [ "$break" != "artifact" ]; then
    mkdir -p "$proj/sessions/$SESS1/subagents/$SUB2"
    printf '{"schema_version":2,"model":"fake","subagent":true}\n{"role":"user","content":"Reply halfway"}\n{"role":"assistant","content":"halfway"}\n' > "$proj/sessions/$SESS1/subagents/$SUB2/transcript.jsonl"
  fi
  printf '{"role":"user","content":"continue the subagent"}\n{"role":"assistant","content":"done"}\n' >> "$H/sessions/$SESS1.jsonl"
fi
cat <<EOF
{"type":"agent_start","model":"fake"}
{"type":"tool_start","name":"agent","args":"{\\"session_id\\":\\"$SUB1\\",\\"prompt\\":\\"Reply with exactly the single word halfway. Do not use any tools.\\"}"}
{"type":"tool_end","name":"agent","result":"[agent: completed] halfway"}
{"type":"text_delta","delta":"done","reasoning":false}
{"type":"turn_end"}
{"type":"agent_end","session_id":"$resume_sid","prompt_tokens":800,"completion_tokens":30,"cache_hit_tokens":400}
EOF
exit 0
OUTER
chmod +x "$fake"

run_eval() { # run_eval RESULTS_DIR EXTRA_ARGS... —— 透传给沙盒 run.sh
  local resdir=$1; shift
  export FAKE_STATE="$tmp/state"
  SEEK_EVAL_RESULTS_DIR="$resdir" bash "$sb/eval/run.sh" "$@" "$CASE" "$fake" 2>"$tmp/stderr"
}

# ---- ①：全过（rounds=1 → 单轮即判定，PASS + exit 0 + 指标逐项） ------
mkdir -p "$tmp/good"
export FAKE_GOOD_ROUNDS=99 FAKE_BREAK=""
: > "$tmp/state"
run_eval "$tmp/good" > "$tmp/good.out" 2>/dev/null
rc=$?
[ "$rc" -eq 0 ] || fail "① good stream exited ${rc} (want 0)"
grep -q '^PASS  subagent-bg-resume  (' "$tmp/good.out" \
  || fail "① missing PASS line: $(head -3 "$tmp/good.out")"
row=$(jq -c 'select(.type != "aggregate")' "$tmp/good/"*.jsonl | head -1)
for kv in \
  'agent_calls:2' 'agent_bg_calls:1' 'agent_continue_calls:1' \
  'bg_started_events:1' 'bg_completed_events:1' 'bg_notified_events:1' \
  'bg_notified_transcript:1' 'bg_resumed_lineage:1' 'bg_artifacts_missing:0'; do
  k=${kv%%:*}; v=${kv##*:}
  got=$(printf '%s' "$row" | jq -r ".metrics.$k // \"ABSENT\"")
  [ "$got" = "$v" ] || fail "① metrics.$k want $v, got $got (data-gated key missing?)"
done

# eval/results 零新增（隔离钉：ablation 行绝不落正式 results）
results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] \
  || fail "eval/results inventory changed during selftest"

# ---- ②：五种断言失败形态逐个判红，失败行点名指标 --------------------
# break=<flavor>: want_metric=grep 片段（防假红——红必须落在具体字段上）
run_break() { # run_break FLAVOR GREP...
  local flavor=$1; shift
  mkdir -p "$tmp/brk-$flavor"
  export FAKE_GOOD_ROUNDS=0 FAKE_BREAK="$flavor"
  : > "$tmp/state"
  run_eval "$tmp/brk-$flavor" > "$tmp/brk-$flavor.out" 2>/dev/null
  local rc=$?
  [ "$rc" -eq 1 ] || fail "② break=$flavor exited ${rc} (want 1)"
  local out
  out=$(cat "$tmp/brk-$flavor.out")
  local pat
  for pat in "$@"; do
    printf '%s\n' "$out" | grep -q "$pat" \
      || fail "② break=$flavor: failure line missing '$pat' — got: $(printf '%s' "$out" | grep -E 'want|min|max' | head -3)"
  done
  printf '%s\n' "$out" | grep -q 'unknown metric' \
    && fail "② break=$flavor: gated metric key went absent (runner_fact channel must be unconditional)"
}

run_break notified \
  'bg_notified_events: want min=1, got 0' \
  'bg_notified_transcript: want min=1, got 0'
run_break resumed \
  'bg_resumed_lineage: want min=1, got 0'
run_break artifact \
  'bg_artifacts_missing: want max=0, got 1'
run_break cancelled \
  'bg_completed_events: want min=1, got 0' \
  'bg_notified_events: want min=1, got 0'
run_break stream \
  'agent_bg_calls: want min=1, got 0' \
  'nonzero_exits: want max=0, got 1'

# ---- ③：record_only —— 只记录不染色 ----------------------------------
mkdir -p "$tmp/rec"
cp "$sb/eval/cases/$CASE/expect.json" "$tmp/expect.bak"
jq '.record_only = true' "$tmp/expect.bak" > "$sb/eval/cases/$CASE/expect.json"
export FAKE_GOOD_ROUNDS=0 FAKE_BREAK=notified
: > "$tmp/state"
run_eval "$tmp/rec" > "$tmp/rec.out" 2>/dev/null
rc=$?
[ "$rc" -eq 0 ] || fail "③ record_only with a failing stream exited ${rc} (want 0 — never gated)"
grep -q '^RECORD  subagent-bg-resume  (0/1 rounds, record-only — not gated)$' "$tmp/rec.out" \
  || fail "③ missing/incorrect RECORD line: $(grep RECORD "$tmp/rec.out")"
cp "$tmp/expect.bak" "$sb/eval/cases/$CASE/expect.json"

# ---- ④：E1 rounds 口径兼容（--rounds 3，前 2 好后 1 坏 → 2/3） -------
export FAKE_GOOD_ROUNDS=2 FAKE_BREAK=artifact
for gate in 0.6 1.0; do
  d="$tmp/agg-${gate}"
  mkdir -p "$d"
  jq --argjson g "$gate" '.min_pass_rate = $g' "$tmp/expect.bak" > "$sb/eval/cases/$CASE/expect.json"
  : > "$tmp/state"
  run_eval "$d" --rounds 3 > "$d.out" 2>/dev/null
  rc=$?
  if [ "$gate" = "0.6" ]; then
    [ "$rc" -eq 0 ] || fail "④ 2/3 vs min 0.6 exited ${rc} (want 0)"
    grep -q '^PASS  subagent-bg-resume  aggregate (2/3 rounds >= min_pass_rate 0.6)$' "$d.out" \
      || fail "④ missing boundary-green aggregate line: $(grep aggregate "$d.out")"
  else
    [ "$rc" -eq 1 ] || fail "④ 2/3 vs min 1.0 exited ${rc} (want 1)"
    grep -q '^FAIL  subagent-bg-resume  aggregate (2/3 rounds < min_pass_rate 1.0)$' "$d.out" \
      || fail "④ missing boundary-red aggregate line: $(grep aggregate "$d.out")"
  fi
  n_round_rows=$(jq -s '[.[] | select(.type != "aggregate")] | length' "$d/"*.jsonl)
  [ "$n_round_rows" = "3" ] || fail "④ want 3 per-round rows, got ${n_round_rows}"
  jq -e -s --argjson g "$gate" 'any(.[]; .type == "aggregate" and .rounds == 3 and .passed == 2 and .pass_rate == (2/3) and .min_pass_rate == $g and .record_only == false)' "$d/"*.jsonl >/dev/null \
    || fail "④ aggregate row fields wrong at gate ${gate}: $(jq -c 'select(.type == "aggregate")' "$d/"*.jsonl)"
done
cp "$tmp/expect.bak" "$sb/eval/cases/$CASE/expect.json"

if [ "$fails" -eq 0 ]; then
  echo "eval-e4-case selftest: PASS (all-green row + 5 named-red flavors + record-only + rounds aggregate compatibility)"
  exit 0
fi
echo "eval-e4-case selftest: FAIL (${fails} assertion(s))" >&2
exit 1
