#!/usr/bin/env bash
# runner.sh — serve-web-stream（eval v2 E5，docs/prd/archive/feature-eval-v2.md
# §2.2/§三-E5，卡 c_lbtdqw）。承重轴用例：真 `seek serve --frontend=web`
# 子进程 → /msg /bind + 提问 → 子进程 seek 跑一轮 → SSE 事件族 →
# /rpc session/list 落盘双断言。
#
# 启动/收尾复用 scripts/serve-smoke-test.sh 先例（c_snerqc 的契约形状）：
#   - 起平面后轮询启动行（web: up:）判活，不猜时序；
#   - token 从 <SEEK_HOME>/serve/web.token 读（ensureToken 落盘）；
#   - 收尾走排干契约：SIGTERM → 排空 → 退出码必须 0（runner_fact 上浮）；
#   - DRY_RUN=1 只打印步骤不动手。
# 差异（也是本例的存在理由）：smoke 断的是未绑定会话的 unboundHint（执行器
# 永不跑）；本例走完整绑定→执行→落盘链路，断言面在 W1.4 SSE 帧族 +
# W4.2 session/list。
#
# 假 key 口径（卡面硬规定，v8 §9.1 无人值守纪律）：子进程的车道在本
# runner 内钉死为 GLM 兼容车道 → 127.0.0.1 假端点（eval/tools/
# fake_openai_compat.py，复用不自造）。为此 serve 进程环境里五大车道
# 凭证一律清空（空值 = os.Getenv 视为未设），GLM_API_KEY/GLM_BASE_URL
# 指向假端点——外层车道旋钮（SEEK_EVAL_*）对本例不生效，是有意为之：
# 本例测的是平面线契约，不是模型选择。真 key 发版道（E6）的接缝 =
# 同一 env-in/env-out 原理：不清空车道凭证、不设 GLM 假端点即可。
#
# stdout = eval JSONL 流：sse_frame 行（SSE 帧配对归一）+ session_list
# 行 + runner_fact 行（进程级结果）。判分全部在 eval/run.sh 的
# extract_metric 里复算（wire 形状，非散文）。
#
# Env contract (eval/run.sh): SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT,
# SEEK_EVAL_PROMPT_FILE, SEEK_EVAL_MAX_TURNS（本 runner 不用——子进程
# argv 由执行器固定）。依赖：curl（SSE 消费）+ python3（假端点）+ jq。
set -u

# DRY_RUN 先于一切 env 契约变量（serve-smoke 同款：裸跑也要能打印步骤）。
if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: python3 eval/tools/fake_openai_compat.py (PORT=<rand>, DELTAS='web plane| stream ok')"
  echo "DRY_RUN: PATH=<sandbox>/bin:\$PATH ${SEEK_EVAL_BINARY:-./seek} serve --frontend=web --listen 127.0.0.1:<rand> (SEEK_HOME=<sandbox>/home, 车道凭证清空+GLM 假车道)"
  echo "DRY_RUN: SSE GET /events?token=..&conv=<id> → POST /msg /bind <sandbox>/proj → POST /msg prompt → 等 run 收口帧"
  echo "DRY_RUN: POST /rpc session/list → 断言 sid 命中；SIGTERM serve → 排干退出 0"
  exit 0
fi

scratch="$SEEK_EVAL_REPO_ROOT/eval/tmp/serve-web-stream"
home_dir="$scratch/home"
proj_dir="$scratch/proj"
bin_dir="$scratch/bin"

command -v curl >/dev/null 2>&1 || { echo "serve-web-stream: curl required" >&2; exit 3; }
command -v python3 >/dev/null 2>&1 || { echo "serve-web-stream: python3 required" >&2; exit 3; }

# 每轮全清（E1 口径：一轮 = 一次完整生命周期；--rounds N 下可重入）。
rm -rf "$scratch"
mkdir -p "$home_dir" "$proj_dir" "$bin_dir"

# 执行器用 PATH 查找 "seek"（executor.go SeekPath 缺省）——把受测二进制
# 以 seek 之名放进沙盒 bin 并置于 PATH 首位，serve 派生的子进程即受测件。
cp "$SEEK_EVAL_BINARY" "$bin_dir/seek"

fake_pid=""
serve_pid=""
curl_pid=""
cleanup() {
  [ -n "$curl_pid" ] && kill "$curl_pid" 2>/dev/null
  [ -n "$serve_pid" ] && kill "$serve_pid" 2>/dev/null
  [ -n "$fake_pid" ] && kill "$fake_pid" 2>/dev/null
  return 0
}
trap cleanup EXIT INT TERM

fact() { # fact <invocation> <exit_code> — 进程级结果上浮给 nonzero_exits
  printf '{"type":"runner_fact","invocation":"%s","exit_code":%s}\n' "$1" "$2"
}

# ---- 假端点：先起（serve 的 GLM_BASE_URL 要指向它） ----------------------
fake_port=$(( 21000 + RANDOM % 8000 ))
fake_url=""
for _ in 1 2 3 4; do
  PORT="$fake_port" DELTAS='web plane| stream ok' \
    python3 "$SEEK_EVAL_REPO_ROOT/eval/tools/fake_openai_compat.py" \
    >"$scratch/fake.log" 2>&1 &
  fake_pid=$!
  sleep 0.4
  # 探活：假端点对任何 POST 都答同一段 SSE（多消费一轮只多一行请求日志）。
  if curl -s -o /dev/null --max-time 3 -X POST "http://127.0.0.1:$fake_port/probe"; then
    fake_url="http://127.0.0.1:$fake_port/chat/completions"
    break
  fi
  kill "$fake_pid" 2>/dev/null; wait "$fake_pid" 2>/dev/null; fake_pid=""
  fake_port=$(( fake_port + 1 ))
done
if [ -z "$fake_url" ]; then
  fact fake_endpoint 1
  exit 0
fi

# ---- serve 平面：随机口重试，'web: up:' 判活 ----------------------------
serve_port=$(( 21000 + RANDOM % 8000 ))
serve_log="$scratch/serve.log"
for _ in 1 2 3 4; do
  env \
    DEEPSEEK_API_KEY= ANTHROPIC_API_KEY= OPENAI_API_KEY= \
    GEMINI_API_KEY= OPENCODE_API_KEY= \
    GLM_API_KEY=fake-key-eval-e5 \
    GLM_BASE_URL="$fake_url" \
    SEEK_HOME="$home_dir" \
    PATH="$bin_dir:$PATH" \
    "$SEEK_EVAL_BINARY" serve --frontend=web --listen "127.0.0.1:$serve_port" \
    >"$scratch/serve.out" 2>"$serve_log" &
  serve_pid=$!
  up=0
  for _ in $(seq 1 40); do
    grep -q 'web: up:' "$serve_log" 2>/dev/null && { up=1; break; }
    kill -0 "$serve_pid" 2>/dev/null || break
    sleep 0.25
  done
  [ "$up" = 1 ] && break
  wait "$serve_pid" 2>/dev/null
  serve_pid=""
  serve_port=$(( serve_port + 1 ))
done
if [ -z "$serve_pid" ]; then
  fact serve_startup 1
  exit 0
fi

tok=$(tr -d ' \n\t' < "$home_dir/serve/web.token" 2>/dev/null || true)
base="http://127.0.0.1:$serve_port"
conv="e5-$$-$RANDOM"

# ---- SSE 订阅先于一切 POST（MVP 对未注册订阅者丢帧） --------------------
sse_log="$scratch/sse.log"
: > "$sse_log"
curl -Ns --max-time 150 "$base/events?token=$tok&conv=$conv" >"$sse_log" 2>/dev/null &
curl_pid=$!
sleep 0.7  # 订阅注册窗口（serve-smoke 同款预算）

post_msg() { # post_msg <text> → HTTP 状态码
  curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$base/msg?token=$tok&conv=$conv" \
    -H 'Content-Type: application/json' --data-binary "$(jq -cn --arg t "$1" '{text:$t}')"
}

# ---- /bind：等「无 run 的默认帧」形状的命令回执（不匹配散文） -----------
# 命令回执形状：data 帧、conv 匹配、无 run 键（run 收口帧才带 run）。
# 必须等到回执再发问：Serve 每条入站各自起 goroutine，不等会让提问
# 抢在绑定写入前查库，吃到 unboundHint 而非一轮真跑。
bind_seen=0
code=$(post_msg "/bind $proj_dir")
[ "$code" = 202 ] || fact post_bind 1
for (( i = 0; i < 200; i++ )); do
  if grep -F "\"conv\":\"web:$conv\"" "$sse_log" 2>/dev/null \
      | grep '^data: ' | grep -v '"run"' | grep -q .; then
    bind_seen=1
    break
  fi
  sleep 0.1
done
if [ "$bind_seen" != 1 ]; then
  fact bind_ack 1
fi

# ---- 提问：等 run 收口帧（带 run + session_id 的默认帧） -----------------
prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
code=$(post_msg "$prompt")
[ "$code" = 202 ] || fact post_prompt 1
# 收口形状：data 帧 + run 键 + session_id 键（崩溃/未绑定都不会有）。
closeout_seen=0
for (( i = 0; i < 1200; i++ )); do
  if grep -F "\"conv\":\"web:$conv\"" "$sse_log" \
      | grep '^data: ' | grep '"run"' | grep -q '"session_id"'; then
    closeout_seen=1
    break
  fi
  sleep 0.1
done
if [ "$closeout_seen" != 1 ]; then
  fact run_closeout 1
fi

# ---- /rpc session/list（落盘轴） ----------------------------------------
rpc_json="$scratch/rpc.json"
: > "$rpc_json"
code=$(curl -s -o "$rpc_json" -w '%{http_code}' --max-time 10 -X POST \
  "$base/rpc?token=$tok" -H 'Content-Type: application/json' \
  --data-binary '{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}')
[ "$code" = 200 ] || fact post_rpc 1

# ---- 收尾：排干契约（serve-smoke 先例）——SIGTERM → 等 → 退出码上浮 -----
kill "$curl_pid" 2>/dev/null; wait "$curl_pid" 2>/dev/null; curl_pid=""
kill -TERM "$serve_pid" 2>/dev/null
drain_rc=1
for (( i = 0; i < 50; i++ )); do
  kill -0 "$serve_pid" 2>/dev/null || break
  sleep 0.2
done
if kill -0 "$serve_pid" 2>/dev/null; then
  kill -9 "$serve_pid" 2>/dev/null
else
  drain_rc=0
fi
wait "$serve_pid" 2>/dev/null
rc=$?
if [ "$drain_rc" = 0 ] && [ "$rc" = 0 ]; then
  fact serve_drain 0
else
  fact serve_drain "${rc:-1}"
fi
serve_pid=""

# ---- 流归一：SSE 原始帧 → sse_frame JSONL（判分材料，判分在 run.sh） ----
# 配对规则 = W1.4 写端形状：命名帧 event:<name> + data:<json>；收口/命令
# 回执是无 event 行的默认帧（event 记 ""）。retry:/心跳注释行不进流。
awk '/^event: /{ev=substr($0,8)}
     /^data: /{printf "%s\t%s\n", ev, substr($0,7); ev=""}' "$sse_log" 2>/dev/null \
  | jq -cR 'split("\t") | {type:"sse_frame", event:.[0], data:(.[1]|fromjson)}' 2>/dev/null

sids=$(jq -c 'try (.result.sessions | map(.sid)) catch []' "$rpc_json" 2>/dev/null \
  || echo '[]')
printf '{"type":"session_list","sids":%s}\n' "$sids"

exit 0
