#!/usr/bin/env bash
#
# web-sessions-manual.sh — W4.4 资产面 UI·一（sessions 浏览器）的手测脚本
# （feature-web-frontend §9 W4.4 验收：「手测脚本；长 transcript 分页不卡」）。
#
# 干什么：临时 SEEK_HOME 里造两个会话夹具——一条 1234 条消息的长
# transcript（含代码围栏/工具调用/2KB 工具结果，模拟真实体积分布）加一条
# 2 条的短会话（助手消息带 <script>/<img onerror> 注入载荷——XSS 用例，
# 验 textContent 纪律在资产面板同样成立）——起 `seek serve --frontend=web`，
# 先跑服务端自检（curl /rpc 的列表/分页契约），再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-sessions-headless.mjs 无头跑
#     全部断言（面板开合/首页 50 条/翻页走完 1234 + 页界连续 + 每页时延/
#     切换无串台/XSS 不执行/回聊天零回归）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790（除非 ADDR 指向它，别这么干）。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 右上「📚 会话」进面板：列表见两条（长会话标题=首条用户消息）
#   2. 点长会话：即刻渲染第 1 页 50 条（不整读 1234 条）；元信息行有
#      model/turns/tokens；滚到底点「加载更多」反复翻——翻页响应及时、
#      滚动不卡（虚拟滚动级量级即可），翻空出现「— 已到末尾 —」
#   3. 中途切短会话再切回：分页游标从头开始，无串台；短会话里的
#      <script>/<img onerror> 显示为纯文本、无执行痕迹
#   4. 点「💬 聊天」返回：输入框/SSE 聊天面原样可用（W4.4 是加法，零回归）
#
# Env:
#   ADDR=127.0.0.1:18790  监听地址（写进临时 config.json 的 serve.web_addr，
#                          只放行 loopback/tailnet 段——生产代码无 env 出口）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1             只打印步骤不动手（butler 自测范式）
#   KEEP=1                结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-sessions-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18790}"   # 默认避开生产 8790——撞生产口与头注释承诺相悖
LONG_SID="20261115-100000-w44longfixture01"
TINY_SID="20261115-100001-w44tinyfixture02"
N_LONG=1234   # 50/页 → 24 整页 + 1 尾页 34 条（验收尾页「已到末尾」路径）

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 造夹具 \$TMP/sessions/{$LONG_SID,$TINY_SID}.jsonl（${N_LONG}+2 条消息，短会话助手消息含 XSS 载荷）"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（stderr 抓 banner URL）"
  echo "DRY_RUN: curl 自检：GET / 200+HTML；session/list 两 sid；messages 首页 50+next_cursor=50；"
  echo "DRY_RUN:   cursor=1200 → 34 条无 next_cursor；短会话 2 条无 next_cursor"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w44-manual.XXXXXX)" || exit 1
SERVE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then
    # ${TMP} 花括号不可省：macOS bash 3.2 在 set -u 下把紧跟多字节字符的
    # $TMP 误读进变量名（$TMP（ → "TMP<EFBBBF>: unbound variable"）。
    echo "KEEP=1：保留 ${TMP}（serve 已停；自行清理）"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

# ---- 1. 构建 ----
echo "== build =="
(cd "$REPO" && go build -o "$TMP/seek" ./cmd/seek) || exit 1

# ---- 2. 夹具：长 transcript + 短会话 ----
# 头行契约 = SaveTo 的 line 1（Session 去掉 messages）；消息行 = deepseek.Message
# 原样 JSON。计数器只影响展示，数值不必精确对账。
echo "== fixtures =="
mkdir -p "$TMP/sessions"
hdr() { # hdr <id> <created> <updated> <turns> <tool_calls> <prompt> <completion>
  printf '{"schema_version":2,"id":"%s","created_at":"%s","updated_at":"%s","model":"deepseek-chat","cwd":"/tmp/w44-fixture","turns":%s,"tool_calls":%s,"usage":{"prompt_tokens":%s,"completion_tokens":%s,"total_tokens":%s,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":%s}}\n' \
    "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$(( $6 + $7 ))" "$6"
}
LONG="$TMP/sessions/$LONG_SID.jsonl"
hdr "$LONG_SID" "2026-11-15T10:00:00+08:00" "2026-11-15T11:30:00+08:00" 617 120 180000 90000 > "$LONG"
# 每轮恰好追加一行（工具调用与工具结果分两轮走）——行数严格 = N_LONG，
# 尾页断言（cursor=1200 → 34 条）依赖这个精确值。
i=0
while [ "$i" -lt "$N_LONG" ]; do
  n=$(( i + 1 ))
  case $(( n % 20 )) in
    1) printf '{"role":"user","content":"第 %s 条：请读一下这段代码并解释。\\n细节在下一行。"}\n' "$n" >> "$LONG"
      ;;
    5) printf '{"role":"assistant","content":"","tool_calls":[{"id":"call_%s","type":"function","function":{"name":"read","arguments":"{\\"path\\":\\"pkg/agent/agent.go\\",\\"offset\\":%s}"}}]}\n' "$n" "$n" >> "$LONG"
      ;;
    6) BIG=$(printf 'x%.0s' $(seq 1 2048))   # 2KB 工具结果——模拟真实体积分布
       printf '{"role":"tool","tool_call_id":"call_%s","content":"package agent // %s"}\n' "$(( n - 1 ))" "$BIG" >> "$LONG"
       ;;
    0) printf '{"role":"assistant","content":"第 %s 条回复：要点如下。\\n```go\\nfunc main() { println(\\"ok\\") }\\n```\\n以上。"}\n' "$n" >> "$LONG"
      ;;
    *) printf '{"role":"assistant","content":"第 %s 条回复：短答复。"}\n' "$n" >> "$LONG"
      ;;
  esac
  i=$(( i + 1 ))
done
LINES=$(( $(wc -l < "$LONG" | tr -d ' ') - 1 ))
[ "$LINES" = "$N_LONG" ] || { echo "FAIL: 夹具行数 $LINES != $N_LONG"; exit 1; }

TINY="$TMP/sessions/$TINY_SID.jsonl"
hdr "$TINY_SID" "2026-11-15T09:00:00+08:00" "2026-11-15T09:05:00+08:00" 1 0 100 50 > "$TINY"
printf '{"role":"user","content":"tiny fixture：你好"}\n' >> "$TINY"
# XSS 用例（验收原文）：助手消息带注入载荷，只走 磁盘→/rpc→页面 textContent
# 这条线——页面源里从不出现，执行与否由无头断言/人工清单核。
printf '{"role":"assistant","content":"tiny fixture：<script>window.__xss=1</script><img src=x onerror=\\"window.__xss2=1\\">上面两段应显示为纯文本。"}\n' >> "$TINY"
wc -l "$LONG" "$TINY"

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

# ---- 5. 服务端自检（/rpc 契约；UI 消费的同一条路）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
rpcc() { # rpcc <method> <params-json> → 响应体（banner URL 去查询串后拼 /rpc）
  curl -s -X POST "${URL%%\?*}rpc?token=${URL#*token=}" \
    -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}

page_count() { printf '%s' "$1" | grep -o '"role"' | wc -l | tr -d ' '; }
has_cursor() { printf '%s' "$1" | grep -o "\"next_cursor\":\"[0-9]*\"" | head -1; }

html=$(curl -s "${URL%%\?*}")
ck "GET / 200+页面" "yes" "$(printf '%s' "$html" | grep -q 'id="browse-btn"' && echo yes || echo no)"

list=$(rpcc session/list '{}')
ck "session/list 含长会话" "yes" "$(printf '%s' "$list" | grep -q "$LONG_SID" && echo yes || echo no)"
ck "session/list 含短会话" "yes" "$(printf '%s' "$list" | grep -q "$TINY_SID" && echo yes || echo no)"

p1=$(rpcc session/messages "{\"sid\":\"$LONG_SID\",\"limit\":50}")
ck "首页 50 条" "50" "$(page_count "$p1")"
ck "首页 next_cursor=50" '"next_cursor":"50"' "$(has_cursor "$p1")"

# cursor 是字符串（W4.2 契约：next message index 的不透明串），数字会被 -32602 拒
pTail=$(rpcc session/messages "{\"sid\":\"$LONG_SID\",\"cursor\":\"1200\",\"limit\":50}")
ck "尾页 34 条" "34" "$(page_count "$pTail")"
ck "尾页无 next_cursor" "" "$(has_cursor "$pTail")"

pTiny=$(rpcc session/messages "{\"sid\":\"$TINY_SID\"}")
ck "短会话 2 条" "2" "$(page_count "$pTiny")"
ck "短会话无 next_cursor" "" "$(has_cursor "$pTiny")"

[ "$fail" = 0 ] || { echo "自检有 FAIL——手测前先修"; exit 1; }

# ---- 6. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = "1" ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-sessions-headless.mjs" "$URL" "$LONG_SID" "$TINY_SID" \
    --chrome "$CHROME_BIN" &
  NODE_PID=$!
  wait "$NODE_PID"; rc=$?
  NODE_PID=""
  [ "$rc" = 0 ] || exit 1
  echo "无头验收全绿。现场已清理（KEEP=1 可保留复跑）"
  exit 0
fi

cat <<EOF

== 无头条件缺（Chrome/node）——人工手测清单（浏览器）==
  打开：$URL
  1. 点「📚 会话」→ 列表两条：长会话标题≈「第 1 条：请读一下这段代码并解释。」，
     短会话「tiny fixture：你好」；新→旧排序
  2. 点长会话：首屏 50 条即刻渲染；元信息行 model/turns/tokens；
     「加载更多」连翻到 1234 条——翻页跟手、滚动不卡；末尾「— 已到末尾 —」
  3. 翻几页后中途切短会话：2 条 + 立即「— 已到末尾 —」（<script>/<img
     onerror> 载荷显示为纯文本，无执行痕迹）；再切回长会话，
     从第 1 页重开、无串台
  4. 「💬 聊天」返回：输入框/状态行/SSE 聊天原样（W4.4 零回归面）
  5. 长按滚动/快速翻页几下：无卡顿（分页外无整读；工具结果限高）

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _

echo "done"
