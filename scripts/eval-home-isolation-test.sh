#!/bin/bash
# eval-home-isolation-test.sh — eval/run.sh 的 SEEK_HOME 沙盒隔离自测
# （card c_clip4i，2026-11-18；脚本自动进 selftest 发现名单，裸跑即自测）。
#
# 为什么存在：run.sh 把全部用户态写入钉进 SEEK_HOME 沙盒是一条结构性
# 保证（卡验收①），但「保证」要么有证据要么没有。本脚本两层断言：
#
#   A 机制层（假二进制；零网络、零 go build）：假二进制把经 run.sh 收到的
#     SEEK_HOME / SEEK_SESSIONS_DIR 写进捕获文件（不能打印到 stdout——
#     那是 run.sh 要拿 jq 消化的事件流），断言——
#       A1 未自带 SEEK_HOME     → 被钉到 <repo>/eval/tmp/seek-home
#       A2 自带非空 SEEK_HOME   → 原样尊重（外层沙盒重定向重定向）
#       A3 残留 SEEK_SESSIONS_DIR → 被剥掉：它在 session.NewStore 的解析
#         阶梯里排在 SEEK_HOME 之上，一个继承来的值会静默绕过沙盒
#
#   B 行为层（真二进制 + 假 key + 金丝雀 HOME）：证明「真 HOME 零写入」
#     不是空转绿——
#       B0 对照组：不走 run.sh、清掉 SEEK_HOME 直接调 seek，金丝雀 HOME
#          的 .seek 必须出现写入（金丝雀灵敏度）。假 key 在第一回合就被
#          端点 401 秒败，seek 仍写 dream-state.json / cron/tick.lock /
#          projects/<id>/manifest.json（2026-11-18 实测）——这正是本卡
#          要杀的污染路径：无人值守车道没有人看输出；
#       B1 隔离组：同一二进制经 run.sh 跑合成用例（runner.sh 故意不带
#          -no-save，与 B0 调用同形，唯一变量是「是否经 run.sh」），
#          金丝雀 HOME 分毫不动，写入全部落 eval/tmp/seek-home；
#       B2 尊重组：SEEK_HOME 自带 → 写入落自带目录，默认沙盒零新增。
#     B 全程 DEEPSEEK_API_KEY=sk-fake-…：无人值守一律假 key（规矩见
#     docs/prd/v8.md §9.1），真端点 401 秒败、零成本。断言不看用例
#     PASS/FAIL（假 key 下 FAIL 本就是预期），只看写入落点。
#
# macOS bash 3.2 兼容（selftest 通道全仓口径）：无 mapfile / 关联数组；
# 默认值展开一律 :- 形态。产物只落 mktemp 沙盒、z 前缀合成用例目录与
# eval/tmp/seek-home（mtime 守卫清理——预存的人肉 eval 数据分毫不动）。
set -u
cd "$(dirname "$0")/.." || exit 2

fails=0
fail() { echo "eval-home-isolation selftest: FAIL: $1" >&2; fails=$((fails+1)); }

command -v jq >/dev/null 2>&1 || { echo "eval-home-isolation selftest: jq required" >&2; exit 2; }
command -v git >/dev/null 2>&1 || { echo "eval-home-isolation selftest: git required" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "eval-home-isolation selftest: not a git repo" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "eval-home-isolation selftest: go required (layer B builds the real binary)" >&2; exit 2; }

# 卡验收①的字面项：脚本本体必须先过语法闸。
bash -n eval/run.sh || { echo "eval-home-isolation selftest: bash -n eval/run.sh failed" >&2; exit 2; }

repo=$(pwd)                       # 与 run.sh 的 cd+pwd 同为逻辑路径口径
CASE=zhome-isolation-selftest     # z 前缀远离真实用例域
SANDBOX=eval/tmp/seek-home        # run.sh 的默认 SEEK_HOME（gitignored）

tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-home-isolation.XXXXXX")
# 规范化（cd+pwd）：macOS 的 TMPDIR 带尾斜杠 → mktemp 返回串含 //，且
# /var 是 /private/var 的符号链接（ablate 自测首版实测踩中）。
tmp=$(cd "$tmp" && pwd) || exit 2
marker="$tmp/marker"
: > "$marker"

cleanup() {
  # 顺序载入语义（ablate 自测实测踩中）：沙盒清理依赖 $tmp/marker 识别
  # 「本测试新落的文件」，必须先于 rm -rf "$tmp" 执行，反过来 marker
  # 先没了、find 静默空转，残留沙盒数据。
  if [ -d "$SANDBOX" ] && [ -f "$marker" ]; then
    find "$SANDBOX" -type f -newer "$marker" -exec rm -f {} + 2>/dev/null
    find "$SANDBOX" -type d -empty -delete 2>/dev/null
  fi
  rm -rf "eval/cases/$CASE" "$tmp"
  rmdir eval/tmp 2>/dev/null
  return 0
}
trap cleanup EXIT

count_files() { # DIR → regular-file count under DIR (0 if absent)
  [ -d "$1" ] || { echo 0; return; }
  find "$1" -type f 2>/dev/null | wc -l | tr -d ' '
}
count_newer() { # DIR MARKER → regular-file count under DIR newer than MARKER
  [ -d "$1" ] || { echo 0; return; }
  find "$1" -type f -newer "$2" 2>/dev/null | wc -l | tr -d ' '
}

# ---- 合成用例：runner 故意不带 -no-save（与 B0 对照组同形） ------------
mkdir -p "eval/cases/$CASE"
printf 'hi\n' > "eval/cases/$CASE/prompt.txt"
printf '{"max_turns":1}\n' > "eval/cases/$CASE/expect.json"
cat > "eval/cases/$CASE/runner.sh" <<'RUNNER'
#!/usr/bin/env bash
# zhome-isolation-selftest runner — default invocation MINUS -no-save:
# forces the full user-state write path (session store init, dream-state,
# cron lock) even though the fake key kills the API call on turn one.
# Contract per eval/README.md: SEEK_EVAL_BINARY / SEEK_EVAL_PROMPT_FILE /
# SEEK_EVAL_MAX_TURNS in env; JSONL event stream to stdout.
set -u
"$SEEK_EVAL_BINARY" -yolo -max-turns "$SEEK_EVAL_MAX_TURNS" -json \
  -p "$(cat "$SEEK_EVAL_PROMPT_FILE")"
RUNNER

# ---- A 机制层：假二进制捕获子进程环境 -----------------------------------
# 版本串 v9.9.9 · deadbee 决定结果文件名（与 ablate 自测同款，确定性）。
# 外层 heredoc 不加引号（要展开 $envcap），其余 $ 一律转义。
envcap="$tmp/envcap"
fake="$tmp/fake-seek"
cat > "$fake" <<OUTER
#!/bin/bash
set -u
if [ "\${1:-}" = "-version" ]; then
  echo "v9.9.9 · deadbee"
  exit 0
fi
{
  echo "SEEK_HOME=\${SEEK_HOME-UNSET}"
  echo "SEEK_SESSIONS_DIR=\${SEEK_SESSIONS_DIR-UNSET}"
} > "$envcap"
cat <<'EOF'
{"type":"agent_end","prompt_tokens":1,"completion_tokens":1,"cache_hit_tokens":0}
EOF
exit 0
OUTER
chmod +x "$fake"

# env 选项顺序（本脚本两次实测踩中同一坑）：-u 必须全部排在第一个
# KEY=VALUE 赋值之前——env 遇到首个非选项参数即停止解析选项，尾置的
# -u 会被当成命令名（rc=127，捕获文件为空）。故下面每条 env 行都按
# 「-u 选项 → KEY=VALUE 赋值 → bash 命令」的固定顺序写。

mkdir -p "$tmp/results-a"

# A1：未自带 → 钉沙盒 + SESSIONS 剥离
: > "$envcap"
env -u SEEK_EVAL_EXTRA_ARGS -u SEEK_HOME -u SEEK_SESSIONS_DIR \
  SEEK_EVAL_RESULTS_DIR="$tmp/results-a" \
  bash eval/run.sh "$CASE" "$fake" >/dev/null 2>&1
grep -q "^SEEK_HOME=$repo/$SANDBOX\$" "$envcap" \
  || fail "A1: child SEEK_HOME not pinned to sandbox (got: $(cat "$envcap" 2>/dev/null | tr '\n' ' '))"
grep -q '^SEEK_SESSIONS_DIR=UNSET$' "$envcap" \
  || fail "A1: SEEK_SESSIONS_DIR should be unset in the child"

# A2：自带非空 → 原样尊重
outer_home="$tmp/outer-home"
: > "$envcap"
env -u SEEK_EVAL_EXTRA_ARGS -u SEEK_SESSIONS_DIR \
  SEEK_HOME="$outer_home" SEEK_EVAL_RESULTS_DIR="$tmp/results-a" \
  bash eval/run.sh "$CASE" "$fake" >/dev/null 2>&1
grep -q "^SEEK_HOME=$outer_home\$" "$envcap" \
  || fail "A2: caller-provided SEEK_HOME not respected (got: $(cat "$envcap" 2>/dev/null | tr '\n' ' '))"

# A3：残留 SEEK_SESSIONS_DIR → 被剥掉（它排在 SEEK_HOME 之上）
: > "$envcap"
env -u SEEK_EVAL_EXTRA_ARGS -u SEEK_HOME \
  SEEK_SESSIONS_DIR=/tmp/definitely-not-the-sessions-dir \
  SEEK_EVAL_RESULTS_DIR="$tmp/results-a" \
  bash eval/run.sh "$CASE" "$fake" >/dev/null 2>&1
grep -q '^SEEK_SESSIONS_DIR=UNSET$' "$envcap" \
  || fail "A3: stray SEEK_SESSIONS_DIR survived into the child (would bypass the sandbox for sessions)"
grep -q "^SEEK_HOME=$repo/$SANDBOX\$" "$envcap" \
  || fail "A3: SEEK_HOME pin broken alongside the SESSIONS strip"

ls "$tmp/results-a/"*.jsonl >/dev/null 2>&1 \
  || fail "A: fake-binary lane wrote no results row — run.sh did not complete"

# ---- B 行为层：真二进制 + 假 key + 金丝雀 HOME ---------------------------
# 无人值守规矩（docs/prd/v8.md §9.1）：本测试自身就是无人值守车道，一律
# 假 key——真端点 401 秒败、零成本，且顺带证明 env-key 解析不依赖沙盒
# 外的 config.json。
FAKEKEY=sk-fake-eval-home-isolation-selftest
go build -o "$tmp/seek-real" ./cmd/seek \
  || { echo "eval-home-isolation selftest: go build failed" >&2; exit 2; }

# git-bash：Go 的 os.UserHomeDir 读 USERPROFILE 而非 bash $HOME，有
# cygpath 就一并钉到金丝雀（mixed 形态，Go 可直接用）；macOS/Linux 无
# cygpath，Go 只读 HOME。UP_ENV 为空时 `env $UP_ENV` 恰好零参数。
up_env() { if command -v cygpath >/dev/null 2>&1; then echo "USERPROFILE=$(cygpath -m "$1")"; fi; }

# B0 对照组：不经 run.sh、无 SEEK_HOME —— 金丝雀必须抓到写入，否则
# B1 的「零写入」是空转绿。
b0="$tmp/b0-home"; b0cwd="$tmp/b0-cwd"
mkdir -p "$b0" "$b0cwd"
( cd "$b0cwd" && env -u SEEK_HOME -u SEEK_SESSIONS_DIR \
    HOME="$b0" $(up_env "$b0") DEEPSEEK_API_KEY="$FAKEKEY" \
    "$tmp/seek-real" -yolo -max-turns 1 -json -p hi ) >/dev/null 2>&1
n=$(count_files "$b0/.seek")
if [ "$n" -lt 1 ]; then
  fail "B0: canary HOME caught no writes — canary insensitive, B1 isolation claim would be vacuous"
fi

# B1 隔离组：同形调用经 run.sh —— 金丝雀分毫不动，写入落沙盒。
b1="$tmp/b1-home"
mkdir -p "$b1"
env -u SEEK_HOME -u SEEK_SESSIONS_DIR \
  HOME="$b1" $(up_env "$b1") DEEPSEEK_API_KEY="$FAKEKEY" \
  SEEK_EVAL_RESULTS_DIR="$tmp/results-b1" \
  bash eval/run.sh "$CASE" "$tmp/seek-real" >/dev/null 2>&1
if [ -e "$b1/.seek" ]; then
  fail "B1: run.sh wrote into the canary HOME: $(find "$b1/.seek" -type f 2>/dev/null | head -3 | tr '\n' ' ')"
fi
n=$(count_newer "$SANDBOX" "$marker")
if [ "$n" -lt 1 ]; then
  fail "B1: sandbox $SANDBOX received no writes — redirect did not engage (or seek stopped writing user state at all)"
fi
ls "$tmp/results-b1/"*.jsonl >/dev/null 2>&1 \
  || fail "B1: no results row — run.sh did not complete"

# B2 尊重组：自带 SEEK_HOME → 写入落自带目录，默认沙盒零新增。
: > "$tmp/marker2"   # 在 B1 之后取戳：B1 的文件不晚于它，B2 的才晚于它
b2="$tmp/b2-home"; b2canary="$tmp/b2-canary"
mkdir -p "$b2" "$b2canary"
env -u SEEK_SESSIONS_DIR \
  HOME="$b2canary" $(up_env "$b2canary") SEEK_HOME="$b2" DEEPSEEK_API_KEY="$FAKEKEY" \
  SEEK_EVAL_RESULTS_DIR="$tmp/results-b2" \
  bash eval/run.sh "$CASE" "$tmp/seek-real" >/dev/null 2>&1
n=$(count_files "$b2")
[ "$n" -ge 1 ] 2>/dev/null || fail "B2: caller-provided SEEK_HOME received no writes (respect path broken?)"
n=$(count_newer "$SANDBOX" "$tmp/marker2")
[ "$n" -eq 0 ] 2>/dev/null || fail "B2: default sandbox gained $n files although SEEK_HOME was caller-set"
[ ! -e "$b2canary/.seek" ] || fail "B2: canary HOME dirtied under caller SEEK_HOME"

if [ "$fails" -eq 0 ]; then
  echo "eval-home-isolation selftest: PASS (bash -n + mechanism A1-A3 + real-binary canary B0-B2)"
  exit 0
fi
echo "eval-home-isolation selftest: FAIL (${fails} assertion(s))" >&2
exit 1
