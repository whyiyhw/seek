#!/usr/bin/env bash
#
# web-subagents-manual.sh — 子代理面板升级（卡 c_4doebx）的手测/无头验收
# 脚本。验收（卡面）：①新 method 单测（列表/详情/不存在 id）——由
# `go test ./internal/rpc/methods -run TestSubagent` 承担；②面板徽标与刷新
# 正确（假数据驱动）——由本脚本 + scripts/web-subagents-headless.mjs 承担；
# ③旧端点回归不破坏——本脚本 curl 自检同时打 /rpc subagent/list 与旧
# GET /subagents，断言两者同源同形。
#
# 干什么：临时 SEEK_HOME 造一个项目（manifest + subagents.jsonl 三行：
# 完成/在跑/失败，各带 transcript 或明确没有），起 `seek serve
# --frontend=web`；先跑服务端自检（curl：/rpc 两个新 method + 旧端点
# 对照 + 不存在 id 的 -32006），再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-subagents-headless.mjs 无头跑
#     全部断言（三态徽标 / 定时刷新 / 详情展开 / 旧端点不动）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 输入 /bind <脚本打印的桩项目路径> 发送 → 收「已绑定项目」
#   2. 点「🤖 后台」：三行记录，徽标三色——「在跑」(黄)、「完成」(绿)、
#      「失败」(红)；后台行带 bg· 前缀
#   3. 点「完成」那行：右侧 meta 一行 + 字段表（sub_sid/parent_sid/状态/
#      类型/模式/开始/结束/tokens/项目）+ 末条 assistant 正文
#   4. 面板开着等 ~10s：脚本在后台追加的第 4 行（失败）自动出现在列表里
#      （定时刷新；不需要手动点）
#   5. 「💬 聊天」返回：输入框/SSE 聊天原样（旧端点与聊天面零回归）
#
# Env:
#   ADDR=127.0.0.1:18796  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl、shasum；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-subagents-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18796}"   # 避开生产 8790 与其它 W 脚本的 18790-18795

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 造夹具 \$TMP/projects/<id>/{manifest.json,subagents.jsonl} + 子 transcript"
  echo "DRY_RUN:   （三行：完成 done-1 / 在跑 bg-1 / 失败 fail-1；追加行 late-1 由无头阶段落盘）"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（stderr 抓 banner URL）"
  echo "DRY_RUN: curl 自检：subagent/list 三行 + project_id；subagent/get 详情+last_assistant；"
  echo "DRY_RUN:   不存在 id → -32006；旧 GET /subagents 同三行（兼容口）"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-c4doebx-manual.XXXXXX)" || exit 1
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

# ---- 2. 夹具：一个项目 + 三行子代理事件 + 一条 transcript ----
# project id = sha256(abs path) 前 16 位 hex（paths.ProjectID 的口径），
# manifest.json 是 ListProjects 认项目的唯一凭据。
echo "== fixtures =="
PROJ="$TMP/proj"
mkdir -p "$PROJ"
PID=$(printf '%s' "$PROJ" | shasum -a 256 | cut -c1-16)
PDIR="$TMP/projects/$PID"
mkdir -p "$PDIR"
printf '{"schema_version":1,"project_id":"%s","abs_path":"%s","first_seen":"2026-11-19T09:00:00Z","last_seen":"2026-11-19T09:30:00Z"}\n' \
  "$PID" "$PROJ" > "$PDIR/manifest.json"

INDEX="$PDIR/subagents.jsonl"
{
  # 完成（终态 + tokens + transcript 尾）
  printf '%s\n' '{"event":"started","sub_sid":"done-1","ts":"2026-11-19T09:00:00Z","parent_sid":"sess-A","parent_turn":2,"type":"explore","description":"find handlers"}'
  printf '%s\n' '{"event":"completed","sub_sid":"done-1","ts":"2026-11-19T09:05:00Z","tokens":{"prompt":100,"completion":20,"cache_hit":80}}'
  # 在跑（无 transcript）
  printf '%s\n' '{"event":"started","sub_sid":"bg-1","ts":"2026-11-19T09:10:00Z","parent_sid":"sess-A","type":"general-purpose","description":"scan deps","background":true}'
  # 失败（带 reason）
  printf '%s\n' '{"event":"started","sub_sid":"fail-1","ts":"2026-11-19T09:20:00Z","parent_sid":"sess-B","type":"plan","description":"outline"}'
  printf '%s\n' '{"event":"failed","sub_sid":"fail-1","ts":"2026-11-19T09:21:00Z","reason":"max_turns_exceeded"}'
} > "$INDEX"

TDIR="$PDIR/sessions/sess-A/subagents/done-1"
mkdir -p "$TDIR"
{
  printf '%s\n' '{"schema_version":2,"id":"h"}'
  printf '%s\n' '{"role":"user","content":"find handlers"}'
  printf '%s\n' '{"role":"assistant","content":"first pass"}'
  printf '%s\n' '{"role":"assistant","content":"panel e2e: final findings"}'
} > "$TDIR/transcript.jsonl"
wc -l "$INDEX" "$TDIR/transcript.jsonl"

# ---- 3. 临时 config：serve.web_addr（生产代码无 env 监听出口，走 config 通道）----
printf '{"serve":{"web_addr":"%s"}}\n' "$ADDR" > "$TMP/config.json"

# ---- 4. 起 serve，抓 banner URL ----
echo "== serve up =="
: > "$TMP/serve.log"
SEEK_HOME="$TMP" "$TMP/seek" serve --frontend=web 2> "$TMP/serve.log" &
SERVE_PID=$!
URL=""
for _ in $(seq 1 50); do
  URL=$(sed -n 's/^web: up: //p' "$TMP/serve.log" | head -1)
  [ -n "$URL" ] && break
  if ! kill -0 "$SERVE_PID" 2>/dev/null; then
    echo "FAIL: serve 提前退出："; cat "$TMP/serve.log"; exit 1
  fi
  sleep 0.2
done
[ -n "$URL" ] || { echo "FAIL: 10s 内未见 banner"; exit 1; }
echo "$URL"
BASE="${URL%%\?*}"; BASE="${BASE%/}"   # 去尾斜杠：//msg 会被 mux 307 重定向
TOK="${URL#*token=}"

# ---- 5. 服务端自检（新 method + 旧端点对照）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF "$2" && echo yes || echo no; }
rpcc() { # rpcc <method> <params-json>
  curl -s -X POST "$BASE/rpc?token=$TOK" \
    -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}

html=$(curl -s "$BASE")
ck "GET / 200+页面" "yes" "$(printf '%s' "$html" | grep -q 'id="agents-btn"' && echo yes || echo no)"
ck "页面挂三态徽标样式" "yes" "$(has "$html" 'st-running')"
ck "页面挂定时刷新接线" "yes" "$(has "$html" 'syncAgentRefresh')"

list=$(rpcc subagent/list "{\"project_id\":\"$PID\"}")
ck "subagent/list project_id 回显" "yes" "$(has "$list" "\"project_id\":\"$PID\"")"
ck "subagent/list 三行" "3" "$(printf '%s' "$list" | grep -o '"sub_sid"' | wc -l | tr -d ' ')"
for sid in done-1 bg-1 fail-1; do
  ck "subagent/list 含 $sid" "yes" "$(has "$list" "$sid")"
done
ck "subagent/list 完成行 tokens" "yes" "$(has "$list" '"tokens":{"prompt":100,"completion":20,"cache_hit":80}')"
ck "subagent/list 失败行 reason" "yes" "$(has "$list" '"reason":"max_turns_exceeded"')"
# 每条响应先落变量再断言——把参数 JSON 嵌进 ck 的 "$( ... )" 会再穿一层
# 引号，`\"` 被吃成裸 `"` 后服务端看到的是坏 JSON（-32700），断言会假红。
failedOnly=$(rpcc subagent/list "{\"project_id\":\"$PID\",\"status\":\"failed\"}")
ck "subagent/list 状态过滤" "1" "$(printf '%s' "$failedOnly" | grep -o '"sub_sid"' | wc -l | tr -d ' ')"
badStatus=$(rpcc subagent/list '{"status":"zombie"}')
ck "subagent/list 未知状态 -32602" "yes" "$(has "$badStatus" '"code":-32602')"

get=$(rpcc subagent/get "{\"sub_sid\":\"done-1\",\"project_id\":\"$PID\"}")
ck "subagent/get 详情行" "yes" "$(has "$get" '"sub_sid":"done-1"')"
ck "subagent/get last_assistant" "yes" "$(has "$get" '"last_assistant":"panel e2e: final findings"')"
ck "subagent/get 项目根" "yes" "$(has "$get" "\"project_root\":\"$PROJ\"")"
activeGet=$(rpcc subagent/get '{"sub_sid":"bg-1"}')
ck "subagent/get 在跑行无 transcript（空串不报错）" "yes" "$(has "$activeGet" '"last_assistant":""')"
missing=$(rpcc subagent/get '{"sub_sid":"nope"}')
ck "subagent/get 不存在 id → -32006" "yes" "$(has "$missing" '"code":-32006')"
tree=$(rpcc subagent/tree '{}')
ck "subagent/tree 未挂（-32601）" "yes" "$(has "$tree" '"code":-32601')"

# 旧端点兼容口：同一份磁盘折叠（先 /bind 让 GET 有项目可读）。
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: subagentfixture' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
legacy=$(curl -s "$BASE/subagents?token=$TOK" -H 'X-Seek-Conversation: subagentfixture')
ck "旧 GET /subagents 仍 200 且三行" "3" "$(printf '%s' "$legacy" | grep -o '"sub_sid"' | wc -l | tr -d ' ')"
for sid in done-1 bg-1 fail-1; do
  ck "旧端点含 $sid" "yes" "$(has "$legacy" "$sid")"
done
legacyDet=$(curl -s "$BASE/subagents?token=$TOK&sub=done-1" -H 'X-Seek-Conversation: subagentfixture')
ck "旧端点 ?sub= 仍给 last_assistant" "yes" "$(has "$legacyDet" '"last_assistant":"panel e2e: final findings"')"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 6. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = "1" ]; then
  echo "== headless（Chrome + node CDP）=="
  # 后台跑无头断言；它等第 4 行时本脚本落盘一条 late-1（failed）——
  # 定时刷新（10s）必须自己把它拉进列表，不靠用户点击。
  "$NODE_BIN" "$HERE/web-subagents-headless.mjs" "$URL" "$PROJ" --chrome "$CHROME_BIN" &
  NODE_PID=$!
  sleep 8
  printf '%s\n' '{"event":"started","sub_sid":"late-1","ts":"2026-11-19T09:40:00Z","parent_sid":"sess-C","type":"explore","description":"late arrival"}' >> "$INDEX"
  printf '%s\n' '{"event":"failed","sub_sid":"late-1","ts":"2026-11-19T09:41:00Z","reason":"canceled"}' >> "$INDEX"
  wait "$NODE_PID"; rc=$?
  NODE_PID=""
  [ "$rc" = 0 ] || exit 1
  echo "无头验收全绿。现场已清理（KEEP=1 可保留复跑）"
  exit 0
fi

cat <<EOF

== 无头条件缺（Chrome/node）——人工手测清单（浏览器）==
  打开：$URL
  1. 发 /bind $PROJ → 收「已绑定项目 …」
  2. 点「🤖 后台」：三行记录；徽标三色——「在跑」(黄) /「完成」(绿) /
     「失败」(红)；后台行带 bg· 前缀
  3. 点「完成」那行：右侧 meta 一行 + 字段表（sub_sid/parent_sid/状态/
     类型/模式/开始/结束/tokens/项目）+ 末条 assistant
     「panel e2e: final findings」
  4. 保持面板开着 ~10s（脚本已追加 late-1 两行）：第 4 行「失败」自动
     出现——定时刷新生效，无需手动操作
  5. 「💬 聊天」返回：输入框/SSE 聊天原样（旧端点与聊天面零回归）

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
