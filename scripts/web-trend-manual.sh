#!/usr/bin/env bash
#
# web-trend-manual.sh — 跨会话趋势图（W5 卡 c_6xktw4）的手测/无头验收
# 脚本。验收（卡面）：①聚合 method 单测——由
# `go test ./internal/rpc/methods -run 'TestStatsTrend|TestSessionList'`
# 承担；②SVG 渲染与悬停交互、空态文案——由本脚本 + 
# scripts/web-trend-headless.mjs 承担（三份夹具：4 会话 / 1 会话 / 空 store）；
# ③size 在配额内（web-gate 过）——由 scripts/web-gate.sh 承担（另跑）；
# ④go test ./... 绿。
#
# 干什么：三趟独立 serve（各自临时 SEEK_HOME，互不串味），每趟先跑 curl
# 自检（stats/trend 的聚合/窗口/边界 + session/list 的 cost_usd），再走浏览器
# 验收：
#   - 本机有 Chrome + node≥22 → scripts/web-trend-headless.mjs 无头跑
#     全部断言（三折线/悬停 tooltip+guide/窗口切换/单点与空态文案/cost 列）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 点「📊 统计」：顶部「跨会话趋势」卡出一条折线（4 个会话）
#   2. 鼠标在图上移动：竖虚线跟随、tooltip 显示 sid/时间/tok/$/缓存%
#   3. 点「近 10 / 近 50 会话」：按钮点亮，图按新窗口重画
#   4. 趋势卡下方仍是单会话快照（前缀缓存/tokens/成本）——趋势是加法
#   5. 左栏每行右侧有绿色 $ 数（服务端算的 cost_usd，页面不携带费率）
#   6. 单会话夹具：一个点 + 「折线需要 ≥2 个点」；空 store：空态文案
#
# Env:
#   ADDR=127.0.0.1:18799  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-trend-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18799}"   # 避开生产 8790 与其它 W 脚本的 18790-18798

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 三趟夹具（multi 4 会话 / single 1 会话 / empty 空 store），各写 config.json serve.web_addr=$ADDR"
  echo "DRY_RUN: 每趟 SEEK_HOME=\$TMP/<mode> \$TMP/seek serve --frontend=web &（stderr 抓 banner URL）"
  echo "DRY_RUN: curl 自检：stats/trend 点数/时间序/hit_ratio/cost_usd；limit 窗口；空 store → {\"points\":[]}；"
  echo "DRY_RUN:   limit 越界 -32602；session/list 每行 cost_usd"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → web-trend-headless.mjs 三景；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-c6xktw4-manual.XXXXXX)" || exit 1
SERVE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then
    # ${TMP} 花括号不可省：macOS bash 3.2 在 set -u 下把紧跟多字节字符的
    # $TMP 误读进变量名（web-sessions-manual.sh 同款坑）。
    echo "KEEP=1：保留 ${TMP}（serve 已停；自行清理）"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

# ---- 1. 构建 ----
echo "== build =="
(cd "$REPO" && go build -o "$TMP/seek" ./cmd/seek) || exit 1

fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
# here-string 而非管道：页面 HTML 很大，grep -q 提前退出会让 printf 吃
# SIGPIPE 刷一行 "write error: Broken pipe" 噪声（断言本身没错，噪声误导人）。
has() { grep -qF -- "$2" <<<"$1" && echo yes || echo no; }
rpcc() { # rpcc <base> <token> <method> <params-json>
  curl -s -X POST "$1/rpc?token=$2" \
    -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$3\",\"params\":$4}"
}

# ---- 2. 夹具生成 ----
# 会话头行 = JSONL line 1（schema_version 3）。第 i 个会话（0 起）：
#   prompt = 1000*(i+1)、completion = 100*(i+1)、cache_hit = 100*i、
#   cache_miss = 1000；updated_at 依次后移 1 分钟 → 最新者 last。
write_session() { # write_session <dir> <idx>
  local dir="$1" i="$2"
  local sid
  sid=$(printf '20261118-09%02d00-%016d' "$i" "$i")
  local p=$((1000 * (i + 1))) c=$((100 * (i + 1)))
  local hit=$((100 * i)) miss=1000
  local total=$((p + c))
  printf '{"schema_version":3,"id":"%s","created_at":"2026-11-18T09:%02d:00Z","updated_at":"2026-11-18T09:%02d:00Z","model":"deepseek-chat","cwd":"/tmp/c6xktw4-fixture","turns":%d,"tool_calls":0,"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_cache_hit_tokens":%d,"prompt_cache_miss_tokens":%d}}\n' \
    "$sid" "$i" "$((i + 1))" "$((i + 1))" "$p" "$c" "$total" "$hit" "$miss" \
    > "$dir/sessions/$sid.jsonl"
  printf '{"role":"user","content":"trend fixture %d"}\n' "$i" >> "$dir/sessions/$sid.jsonl"
  printf '{"role":"assistant","content":"ok %d"}\n' "$i" >> "$dir/sessions/$sid.jsonl"
  echo "$sid"
}

make_home() { # make_home <mode> <count>
  local mode="$1" n="$2"
  local home="$TMP/$mode"
  mkdir -p "$home/sessions"
  local i=0
  while [ "$i" -lt "$n" ]; do write_session "$home" "$i" > /dev/null; i=$((i + 1)); done
  printf '{"serve":{"web_addr":"%s"}}\n' "$ADDR" > "$home/config.json"
  echo "$home"
}

# ---- 3. 一趟：起 serve → 自检 → 浏览器/人工 ----
# start_serve 设全局 URL/BASE/TOK（不 echo、不走 $(...) ——命令替换会等
# 后台 serve 关掉继承的 stdout，直接挂死；既有 W 脚本同样手法）。
start_serve() { # start_serve <home>；失败退 1
  local home="$1"
  : > "$home/serve.log"
  SEEK_HOME="$home" "$TMP/seek" serve --frontend=web 2> "$home/serve.log" &
  SERVE_PID=$!
  URL=""
  local _
  for _ in $(seq 1 50); do
    URL=$(sed -n 's/^web: up: //p' "$home/serve.log" | head -1)
    [ -n "$URL" ] && break
    if ! kill -0 "$SERVE_PID" 2>/dev/null; then
      echo "FAIL: serve 提前退出："; cat "$home/serve.log"; return 1
    fi
    sleep 0.2
  done
  [ -n "$URL" ] || { echo "FAIL: 10s 内未见 banner"; return 1; }
  BASE="${URL%%\?*}"; BASE="${BASE%/}"
  TOK="${URL#*token=}"
  return 0
}

stop_serve() {
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  SERVE_PID=""
}

CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
run_browser() { # run_browser <url> <mode>
  if [ "$HEADLESS" = "1" ]; then
    "$NODE_BIN" "$HERE/web-trend-headless.mjs" "$1" "$2" --chrome "$CHROME_BIN"
    return $?
  fi
  return 9   # 无头条件缺：调用方退人工清单
}

# ========== multi：4 个会话 ==========
echo "== fixtures: multi (4 sessions) =="
HOME_M=$(make_home multi 4)
start_serve "$HOME_M" || exit 1
URL_M="$URL"
echo "$URL_M"

echo "== self-checks: multi =="
html=$(curl -s "$BASE")
ck "GET / 页面（统计入口）" "yes" "$(has "$html" 'id="stat-btn"')"
ck "页面挂趋势卡宿主" "yes" "$(has "$html" 'id="trend-plot"')"
ck "页面挂 stats/trend 接线" "yes" "$(has "$html" 'stats/trend')"
ck "页面挂窗口档位" "yes" "$(has "$html" 'TREND_WINDOWS')"

trend=$(rpcc "$BASE" "$TOK" stats/trend '{}')
ck "stats/trend 四点" "4" "$(printf '%s' "$trend" | grep -o '"sid"' | wc -l | tr -d ' ')"
# 时间序：oldest first → 第一个点应是 idx 0（sid 尾部 ...0000）
first=$(printf '%s' "$trend" | grep -o '"sid":"[^"]*"' | head -1)
ck "stats/trend oldest-first" "yes" "$(printf '%s' "$first" | grep -q -- '-0000000000000000"' && echo yes || echo no)"
lastP=$(printf '%s' "$trend" | grep -o '"sid":"[^"]*"' | tail -1)
ck "stats/trend 末点 = 最新会话" "yes" "$(printf '%s' "$lastP" | grep -q -- '-0000000000000003"' && echo yes || echo no)"
ck "stats/trend 带 hit_ratio" "yes" "$(has "$trend" '"hit_ratio":')"
ck "stats/trend 带 cost_usd" "yes" "$(has "$trend" '"cost_usd":')"
ck "stats/trend 带 token 字段" "yes" "$(has "$trend" '"prompt_tokens":')"
# 最新会话（idx 3）：prompt 4000 / completion 400 / hit 300 / miss 1000 → 300/1300
ck "末点 hit_ratio = 300/1300（0.2307…）" "yes" "$(printf '%s' "$trend" | grep -qE '"hit_ratio":0\.23076' && echo yes || echo no)"

win=$(rpcc "$BASE" "$TOK" stats/trend '{"limit":2}')
ck "stats/trend limit=2 两点" "2" "$(printf '%s' "$win" | grep -o '"sid"' | wc -l | tr -d ' ')"
bad=$(rpcc "$BASE" "$TOK" stats/trend '{"limit":0}')
ck "stats/trend limit=0 → -32602" "yes" "$(has "$bad" '"code":-32602')"
bad2=$(rpcc "$BASE" "$TOK" stats/trend '{"limit":1000}')
ck "stats/trend limit 超上限 → -32602" "yes" "$(has "$bad2" '"code":-32602')"

list=$(rpcc "$BASE" "$TOK" session/list '{}')
ck "session/list 每行带 cost_usd" "4" "$(printf '%s' "$list" | grep -o '"cost_usd"' | wc -l | tr -d ' ')"
ck "session/list cost_usd 非零（有 usage）" "yes" "$(printf '%s' "$list" | grep -qE '"cost_usd":0\.0*[1-9]' && echo yes || echo no)"

if [ "$fail" != 0 ]; then echo "自检有 FAIL——浏览器验收前先修"; exit 1; fi

if run_browser "$URL_M" multi; then
  echo "multi：无头全绿"
else
  rc=$?
  [ "$rc" = 9 ] || { stop_serve; exit 1; }
  MANUAL_LIST=1
fi
stop_serve

# ========== single：1 个会话 ==========
echo "== fixtures: single (1 session) =="
HOME_S=$(make_home single 1)
start_serve "$HOME_S" || exit 1
URL_S="$URL"
echo "$URL_S"
one=$(rpcc "$BASE" "$TOK" stats/trend '{}')
ck "single：stats/trend 一点" "1" "$(printf '%s' "$one" | grep -o '"sid"' | wc -l | tr -d ' ')"
[ "$fail" = 0 ] || { echo "single 自检 FAIL"; exit 1; }
if run_browser "$URL_S" single; then
  echo "single：无头全绿"
else
  rc=$?
  [ "$rc" = 9 ] || { stop_serve; exit 1; }
  MANUAL_LIST=1
fi
stop_serve

# ========== empty：空 store ==========
echo "== fixtures: empty (0 sessions) =="
HOME_E=$(make_home empty 0)
start_serve "$HOME_E" || exit 1
URL_E="$URL"
echo "$URL_E"
empty=$(rpcc "$BASE" "$TOK" stats/trend '{}')
ck "empty：stats/trend 空数组（成功，不是 -32001）" '{"points":[]}' "$(printf '%s' "$empty" | grep -o '{"points":\[\]}' | head -1)"
ck "empty：响应无 error 成员" "no" "$(has "$empty" '"error"')"
if run_browser "$URL_E" empty; then
  echo "empty：无头全绿"
else
  rc=$?
  [ "$rc" = 9 ] || { stop_serve; exit 1; }
  MANUAL_LIST=1
fi

# 无头条件缺 + 非 TTY = 无人值守车道：人工清单无处可走（c_kwiajf 口径）。
if [ "${MANUAL_LIST:-0}" = "1" ]; then
  if [ ! -t 0 ]; then
    miss=""
    [ -x "$CHROME_BIN" ] || miss="Chrome（${CHROME_BIN}）"
    [ -n "$NODE_BIN" ] || miss="${miss:+${miss} + }node"
    echo "FAIL: 无头验收条件缺（${miss}）且 stdin 非 TTY——无人值守没有人工清单可走。装上缺的组件或 CHROME=<路径> 重跑。" >&2
    exit 3
  fi
  cat <<EOF

== 无头条件缺（Chrome/node）——人工手测清单（浏览器）==
  multi（4 会话）：$URL_M
    1. 点「📊 统计」：顶部「跨会话趋势」卡出一条折线（三条：tok / \$ / 缓存）
    2. 鼠标在图上移动：竖虚线跟随，tooltip 显示 sid · 时间 · tok · \$ · 缓存%
    3. 点「近 10 / 近 50 会话」：按钮点亮，图按新窗口重画（夹具 4 个点不变）
    4. 趋势卡下方仍是单会话快照（前缀缓存 / 累计 tokens / 成本）——趋势是加法
    5. 左栏每行右侧有绿色 \$ 数（服务端 cost_usd，页面不带费率）
  single（1 会话）：$URL_S
    6. 图里只有 1 个点、无折线，文案「（只有 1 个会话——折线需要 ≥2 个点）」
  empty（空 store）：$URL_E
    7. 趋势卡空态「（还没有历史会话——先在终端里跑几个会话）」，快照区同口径，不报错

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
  read -r _
fi

echo "done"
