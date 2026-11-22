#!/bin/bash
# eval-e3-case-test.sh — plan-e2e 承重轴用例的确定性自测（eval v2 E3
# 返工 C 案，c_c5wwey，2026-11-21；scripts/*.sh 自动进 selftest 发现制
# 名单，裸跑即自测，零登记）。
#
# 为什么存在：同 eval-e4-case-test.sh 的出生事故教训——plan-e2e 的验收
# 若只靠沙盒手跑真二进制（E3 首轮即如此，18s/轮 + go build），验证车道
# 事后无法机械复跑：三态实跑的入口是 `bash eval/run.sh plan-e2e <真二进
# 制>`，这条命令不在验证车道唯一白名单 Bash(bash scripts/selftest.sh:*)
# 里（selftest.sh 头注释，c_ck6djl 形态），且 run.sh 默认把结果行写进
# eval/results/（run.sh 的 results_dir 逻辑）——真跑面对正式 results 是
# 写入而非只读。本脚本把三态判定收进受审代码、走白名单入口分发：真
# runner.sh + 真 drive.py（PTY）+ 真 fake_plan_endpoint.py + 真 judge 全
# 部原样参与，唯一替换的是 seek 二进制——假二进制吃同样的 argv 形状
# （-version / -plan … -base-url … / -resume SID …），对 endpoint 的
# SSE 响照单执行（tool_calls 原样落 session、参数原样落文件系统）、
# 发同样的 plan 工具链 wire-format 前缀，PTY 面走 drive.py 的完整状态
# 机（placeholder → 输入 → Go ahead picker → done 标记 → /exit）。测
# 的是用例栈的判定语义，不是真 TUI——真二进制全链跑仍归 release lane。
#
#   ① 全过：好链两车道判绿（exit 0），行内 runner_assert_failures=0 /
#      nonzero_exits=0，PASS 行带指标对象；
#   ② 防假红：四种断言失败形态逐个判红（exit 1），失败行点名
#      runner_assert_failures / nonzero_exits 的具体 got 值，行内
#      failures 数组逐条断言（形态断言，不碰 judge 散文措辞）：
#        fold       = resume 段 plan(complete,2) 答 no active plan
#                     （A7 折叠探针红；同时钉死 endpoint 的 3 次重试
#                      上限——resume 恰好 3 次 plan 调用后放弃出
#                      RESUME-E2E-DONE，首跑 204 轮失控的反向锁）
#        no-write   = write 工具结果照发、文件不落盘（A5/A6 红）
#        premature  = 批准前偷跑一次 write（A3 消息序红）
#        marker     = propose 结果前缀不是 [plan: approved]（A2 红，
#                      A3 因无批准行连带红）
#   ③ record_only（用例真默认态）：RECORD 行、exit 0、汇总分列
#      recorded——红轮不闸门；
#   ④ E1 rounds 口径兼容：--rounds 3、前 2 轮好后 1 轮坏（fold）→
#      2/3；对 0.6 聚合判绿（exit 0）、对 1.0 聚合判红（exit 1）；3 行
#      轮次行带 round 字段 + 1 行聚合行，verdict/passed/rounds 逐字段核。
#
# 假二进制契约：python3（stdlib），按 argv 有无 -resume 分辨车道，
# FAKE_STATE/FAKE_GOOD_ROUNDS/FAKE_BREAK 按第 1 车道调用计数切换破坏形
# 态（E4 同款）。session JSONL 落 SEEK_HOME/sessions/<sid>.jsonl，工具
# 结果行不带 name 字段（与真实持久化形状一致——judge 的 toolNameAt 配
# 对与 plan/reconstruct.go 同源）；notes.md 落当前目录（runner 已 cd 到
# fixture workspace）。
#
# macOS bash 3.2 兼容：无 mapfile / 关联数组。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-e3-case selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-e3-case selftest: jq required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "eval-e3-case selftest: python3 required" >&2; exit 2; }

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-e3-case.XXXXXX")
tmp=$(cd "$tmp" && pwd) || exit 2
cleanup() {
  rm -rf "$tmp"
  return 0
}
trap cleanup EXIT

results_before=$(find eval/results 2>/dev/null | sort)

CASE=plan-e2e

# ---- 沙盒仓：run.sh 副本 + 真用例目录 + 真工具三件套原样拷入 --------
sb="$tmp/sandbox"
mkdir -p "$sb/eval/cases" "$sb/eval/tools"
cp eval/run.sh "$sb/eval/run.sh"
cp -R "eval/cases/$CASE" "$sb/eval/cases/$CASE"
cp eval/tools/fake_plan_endpoint.py \
   eval/tools/plan-e2e-drive.py \
   eval/tools/plan-e2e-judge.py \
   "$sb/eval/tools/"

# ---- 假二进制（python3）：说同款 argv/wire 契约的脚本化 seek ----------
fake="$tmp/fake-seek"
cat > "$fake" <<'OUTER'
#!/usr/bin/env python3
import json
import os
import sys
import urllib.request


def out(s):
    print(s, flush=True)


def sse_call(url, msgs):
    """POST the session to the scripted endpoint, parse its SSE chunks
    into (cid, tool_name, args, text). Same wire the real compatible
    adapter speaks; the endpoint is the case's own fixture."""
    body = json.dumps({"model": "plan-e2e-fake", "messages": msgs}).encode()
    req = urllib.request.Request(
        url, data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as r:
        data = r.read().decode()
    cid = ""
    name = None
    args = {}
    text = ""
    for line in data.splitlines():
        if not line.startswith("data: ") or line.strip() == "data: [DONE]":
            continue
        chunk = json.loads(line[6:])
        delta = (chunk.get("choices") or [{}])[0].get("delta") or {}
        for tc in delta.get("tool_calls") or []:
            if tc.get("id"):
                cid = tc["id"]
            fn = tc.get("function") or {}
            if fn.get("name"):
                name = fn["name"]
            if fn.get("arguments"):
                try:
                    args = json.loads(fn["arguments"])
                except ValueError:
                    args = {}
        if delta.get("content"):
            text += delta["content"]
    return cid, name, args, text


def main():
    argv = sys.argv[1:]
    if argv and argv[0] == "-version":
        out("v9.9.9 · e3beef")
        return 0
    base = ""
    resume_sid = ""
    i = 0
    while i < len(argv):
        if argv[i] == "-base-url" and i + 1 < len(argv):
            base = argv[i + 1]
        elif argv[i] == "-resume" and i + 1 < len(argv):
            resume_sid = argv[i + 1]
        i += 1

    # Round-flavored breaks, E4's FAKE_STATE pattern: the fresh-lane
    # invocation bumps the counter, the resume invocation only reads it.
    state = os.environ.get("FAKE_STATE", "")
    good = int(os.environ.get("FAKE_GOOD_ROUNDS", "99"))
    brk = os.environ.get("FAKE_BREAK", "")
    n = 0
    if state:
        try:
            n = int(open(state).read().strip() or "0")
        except (OSError, ValueError):
            n = 0
    if not resume_sid:
        n += 1
        if state:
            with open(state, "w") as f:
                f.write(str(n))
    active = brk if (brk and n > good) else ""

    home = os.environ["SEEK_HOME"]
    os.makedirs(os.path.join(home, "sessions"), exist_ok=True)
    sid = resume_sid or "20261121-120000-e3fake000001"
    spath = os.path.join(home, "sessions", sid + ".jsonl")

    msgs = []
    if resume_sid:
        with open(spath) as f:
            for line in f:
                line = line.strip()
                if line:
                    msgs.append(json.loads(line))
    else:
        msgs = [{"schema_version": 2, "model": "plan-e2e-fake",
                 "created_at": "2026-11-21T12:00:00Z"}]

    def save():
        with open(spath, "w") as f:
            for m in msgs:
                f.write(json.dumps(m) + "\n")

    def call(cid_, name_, args_):
        # arguments ride as a JSON STRING (OpenAI wire shape) — the
        # judge re-parses them exactly like the reconstructor does.
        msgs.append({"role": "assistant", "tool_calls": [
            {"id": cid_, "type": "function",
             "function": {"name": name_,
                          "arguments": json.dumps(args_)}}]})

    def result(cid_, content):
        # No `name` field: the persisted-shape fact E3's first run
        # caught (Message.Name omitempty) — judge pairs by tool_call_id.
        msgs.append({"role": "tool", "tool_call_id": cid_, "content": content})

    # --- PTY dance, stage 1: paint the input placeholder -----------------
    out("seek  ·  Ask seek anything")
    prompt_line = sys.stdin.readline()
    if not prompt_line.strip():
        return 1
    msgs.append({"role": "user", "content": prompt_line.rstrip("\n")})
    save()

    total_steps = 2  # from propose args once seen

    while True:
        cid, name, args, text = sse_call(base, msgs[1:])
        if name is None:
            msgs.append({"role": "assistant", "content": text})
            save()
            out(text)  # the done marker reaches the driver
            break
        if name == "propose":
            total_steps = max(2, len(args.get("steps") or []))
            call(cid, "propose", args)
            save()
            if active == "premature":
                # A write smuggled BEFORE the approval line: A3's
                # message-order fact must catch it.
                w = {"path": "notes.md", "content": "FIXTURE-NOTES-OK\n"}
                call("c-prew", "write", w)
                open(os.path.join(os.getcwd(), w["path"]), "w").write(w["content"])
                result("c-prew", "ok")
                save()
            # The approval picker — TUI-only surface, answered by the
            # driver with Down+Enter (row 1: approve auto-per-step).
            out("  Go ahead?")
            out("  > approve (ask per call)")
            out("    approve — auto-approve per step")
            sys.stdin.readline()
            marker = ("[plan: approved] (auto-approve-per-step)"
                      if active != "marker"
                      else "[plan: rejected] (selftest break: marker)")
            result(cid, marker)
            save()
        elif name == "plan":
            call(cid, "plan", args)
            idx = int(args.get("index") or 1)
            if args.get("action") == "start":
                res = "[plan: step %d started]" % idx
            elif resume_sid and active == "fold":
                res = "Error: no active plan (selftest break: fold)"
            else:
                res = "[plan: step %d complete, %d/%d done]" % (
                    idx, idx, total_steps)
            result(cid, res)
            save()
        elif name == "write":
            call(cid, "write", args)
            if active != "no-write":
                open(os.path.join(os.getcwd(), args.get("path", "notes.md")),
                     "w").write(args.get("content", ""))
            result(cid, "ok")
            save()
        else:
            call(cid, name, args)
            result(cid, "ok")
            save()

    sys.stdin.readline()  # "/exit"
    return 0


if __name__ == "__main__":
    sys.exit(main())
OUTER
chmod +x "$fake"

run_eval() { # run_eval RESULTS_DIR EXTRA_ARGS... —— 透传给沙盒 run.sh
  local resdir=$1; shift
  export FAKE_STATE="$tmp/state"
  SEEK_EVAL_RESULTS_DIR="$resdir" bash "$sb/eval/run.sh" "$@" "$CASE" "$fake" 2>"$tmp/stderr"
}

# expect.json 在各态间被改写，进出都从备份恢复（E4 同款纪律）。
cp "$sb/eval/cases/$CASE/expect.json" "$tmp/expect.bak"

# ---- ①：全过（摘 record_only → 闸门态） ---------------------------------
mkdir -p "$tmp/good"
export FAKE_GOOD_ROUNDS=99 FAKE_BREAK=""
: > "$tmp/state"
jq 'del(.record_only)' "$tmp/expect.bak" > "$sb/eval/cases/$CASE/expect.json"
run_eval "$tmp/good" > "$tmp/good.out" 2>/dev/null
rc=$?
[ "$rc" -eq 0 ] || fail "① good chain exited ${rc} (want 0): $(head -3 "$tmp/good.out")"
grep -q '^PASS  plan-e2e  (' "$tmp/good.out" \
  || fail "① missing PASS line: $(head -3 "$tmp/good.out")"
row=$(jq -c 'select(.type != "aggregate")' "$tmp/good/"*.jsonl | head -1)
[ -n "$row" ] || fail "① no results row written"
for kv in 'runner_assert_failures:0' 'nonzero_exits:0'; do
  k=${kv%%:*}; v=${kv##*:}
  got=$(printf '%s' "$row" | jq -r ".metrics.$k // \"ABSENT\"")
  [ "$got" = "$v" ] || fail "① metrics.$k want $v, got $got"
done
printf '%s' "$row" | jq -e '.pass == true and .failures == []' >/dev/null \
  || fail "① row pass/failures wrong: $(printf '%s' "$row" | jq -c '{pass, failures}')"
# 折叠探针真过：judge A7 的绿已含在 0 failures 里；这里再从行上排除
# resume_same_session 假绿——它由 runner 无条件发 failed:false fact。
printf '%s' "$row" | jq -e '.metrics.turns == 0 and .metrics.agent_ends == 0' >/dev/null \
  || fail "① TUI case must source metrics from runner facts, not stream events"

# eval/results 零新增（隔离钉：自测行绝不落正式 results）
results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] \
  || fail "eval/results inventory changed during selftest"

# ---- ②：四种断言失败形态逐个判红，失败行点名指标与 got 值 --------------
# break=<flavor>: want_failures=<got 值>（judge 红断言数，机械可推）
run_break() { # run_break FLAVOR WANT_ASSERT_REDS
  local flavor=$1 want=$2
  mkdir -p "$tmp/brk-$flavor"
  export FAKE_GOOD_ROUNDS=0 FAKE_BREAK="$flavor"
  : > "$tmp/state"
  run_eval "$tmp/brk-$flavor" > "$tmp/brk-$flavor.out" 2>/dev/null
  local rc=$?
  [ "$rc" -eq 1 ] || fail "② break=$flavor exited ${rc} (want 1)"
  grep -q "^      runner_assert_failures: want max=0, got ${want}\$" "$tmp/brk-$flavor.out" \
    || fail "② break=$flavor: failure line missing 'runner_assert_failures: want max=0, got ${want}' — got: $(grep -E 'want' "$tmp/brk-$flavor.out" | head -3)"
  grep -q '^      nonzero_exits: want max=0, got 1$' "$tmp/brk-$flavor.out" \
    || fail "② break=$flavor: judge exit must surface via nonzero_exits — got: $(grep nonzero "$tmp/brk-$flavor.out" | head -2)"
  local rcount
  rcount=$(jq -s '[.[] | select(.type != "aggregate")][0].metrics.runner_assert_failures // "ABSENT"' "$tmp/brk-$flavor/"*.jsonl)
  [ "$rcount" = "$want" ] || fail "② break=$flavor: row runner_assert_failures want $want, got $rcount"
  local passv
  passv=$(jq -s '[.[] | select(.type != "aggregate")][0].pass' "$tmp/brk-$flavor/"*.jsonl)
  [ "$passv" = "false" ] || fail "② break=$flavor: row pass must be false"
}
run_break fold 1
run_break no-write 2
run_break premature 1
run_break marker 2

# fold 的 3 次重试上限（endpoint 反失控闸）从产物面不可回收（teardown
# 清场 + out_file 即删），改为直读一次 judge 产物前的会话形状：跑一轮
# fold 后由 runner 亲笔事实兜底——若上限失效，nonzero_exits 会随轮数膨
# 胀而不是 1，已被上面 fold 的 got 1 间接钉住（重试无上限时 drive 走
# 不到 done，A10 红、judge 退出码 >1）。此处不另设断言。

# ---- ③：record_only —— 用例真默认态，只记录不染色 ----------------------
mkdir -p "$tmp/rec"
cp "$tmp/expect.bak" "$sb/eval/cases/$CASE/expect.json"
export FAKE_GOOD_ROUNDS=0 FAKE_BREAK=fold
: > "$tmp/state"
run_eval "$tmp/rec" > "$tmp/rec.out" 2>/dev/null
rc=$?
[ "$rc" -eq 0 ] || fail "③ record_only with a failing round exited ${rc} (want 0 — never gated)"
grep -q '^RECORD  plan-e2e  (0/1 rounds, record-only — not gated)$' "$tmp/rec.out" \
  || fail "③ missing/incorrect RECORD line: $(grep RECORD "$tmp/rec.out")"
grep -q '^eval: 0 passed, 0 failed, 1 recorded (record-only)$' "$tmp/rec.out" \
  || fail "③ summary must split recorded column: $(grep 'eval:' "$tmp/rec.out" | tail -1)"
jq -e -s 'any(.[]; .type != "aggregate" and .pass == false and .metrics.runner_assert_failures == 1)' "$tmp/rec/"*.jsonl >/dev/null \
  || fail "③ recorded row must keep the red judgement (record ≠ rewrite)"

# ---- ④：E1 rounds 口径兼容（--rounds 3，前 2 好后 1 坏 → 2/3） ---------
export FAKE_GOOD_ROUNDS=2 FAKE_BREAK=fold
for gate in 0.6 1.0; do
  d="$tmp/agg-${gate}"
  mkdir -p "$d"
  jq --argjson g "$gate" 'del(.record_only) | .min_pass_rate = $g' \
    "$tmp/expect.bak" > "$sb/eval/cases/$CASE/expect.json"
  : > "$tmp/state"
  run_eval "$d" --rounds 3 > "$d.out" 2>/dev/null
  rc=$?
  if [ "$gate" = "0.6" ]; then
    [ "$rc" -eq 0 ] || fail "④ 2/3 vs min 0.6 exited ${rc} (want 0): $(tail -3 "$d.out")"
    grep -q '^PASS  plan-e2e  aggregate (2/3 rounds >= min_pass_rate 0.6)$' "$d.out" \
      || fail "④ missing boundary-green aggregate line: $(grep aggregate "$d.out")"
  else
    [ "$rc" -eq 1 ] || fail "④ 2/3 vs min 1.0 exited ${rc} (want 1)"
    grep -q '^FAIL  plan-e2e  aggregate (2/3 rounds < min_pass_rate 1.0)$' "$d.out" \
      || fail "④ missing boundary-red aggregate line: $(grep aggregate "$d.out")"
  fi
  n_round_rows=$(jq -s '[.[] | select(.type != "aggregate")] | length' "$d/"*.jsonl)
  [ "$n_round_rows" = "3" ] || fail "④ want 3 per-round rows, got ${n_round_rows}"
  rounds_seq=$(jq -s -r '[.[] | select(.type != "aggregate") | .round] | sort | join(",")' "$d/"*.jsonl)
  [ "$rounds_seq" = "1,2,3" ] || fail "④ round values wrong: ${rounds_seq}"
  jq -e -s --argjson g "$gate" 'any(.[]; .type == "aggregate" and .rounds == 3 and .passed == 2 and .pass_rate == (2/3) and .min_pass_rate == $g and .record_only == false)' "$d/"*.jsonl >/dev/null \
    || fail "④ aggregate row fields wrong at gate ${gate}: $(jq -c 'select(.type == "aggregate")' "$d/"*.jsonl)"
done
cp "$tmp/expect.bak" "$sb/eval/cases/$CASE/expect.json"

# 收尾再钉一次正式 results 零污染（④ 之后复验）。
results_after=$(find eval/results 2>/dev/null | sort)
[ "$results_before" = "$results_after" ] \
  || fail "eval/results inventory changed during selftest (post-④)"

if [ "$fails" -eq 0 ]; then
  echo "eval-e3-case selftest: PASS (all-green row + 4 named-red flavors + record-only + rounds aggregate compatibility — via real runner/drive/endpoint/judge, fake binary only)"
  exit 0
fi
echo "eval-e3-case selftest: FAIL (${fails} assertion(s))" >&2
exit 1
