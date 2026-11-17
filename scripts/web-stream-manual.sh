#!/usr/bin/env bash
#
# web-stream-manual.sh — W2.1 流式气泡 delta 累积→reply 替换 + 工具行
# 折叠的手测/无头验收脚本（feature-web-frontend §9 W2.1 验收：「手测脚本
# （浏览器/headless）；XSS 用例（模型吐 <script> 转义——textContent 纪律
# 保持，无 innerHTML）」）。
#
# 干什么：临时 SEEK_HOME + 桩 seek（PATH 前置劫持——serve 无 SeekPath 出
# 口，executor 按名字找 seek）+ 桩项目目录，起 `seek serve --frontend=web`；
# 先跑服务端自检（curl：页面部件 / 无 HTML 解析 sink / SSE 事件族与
# §2.5 过滤——args/result、reasoning 不下发），再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-stream-headless.mjs 无头跑全
#     部断言（流式过程态/收口替换/XSS 不执行/cancel 收口）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。桩场景按消息文本切换：流式 / 注入 /
# 卡住 / 其它（默认应答）。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 输入 /bind <脚本打印的桩项目路径> 发送 → 收「已绑定项目」
#   2. 发「流式」：先见 ⚙ read … 工具行 + 带▍光标的部分文本逐段上屏；
#      收口后整泡替换成终文本（``` 围栏渲染成代码块），工具行消失
#   3. 发「注入」：<script>/<img onerror> 全部显示为纯文本，页面无弹窗、
#      无新增脚本执行痕迹
#   4. 发「卡住」→ 见 stuck 气泡后发 /cancel：气泡被「已中断 当前 run」
#      收口替换，随后「run 已中断。」独立成条
#   5. 顶栏状态行随指令即时刷新（/bind 后变「已绑定 …」）
#
# Env:
#   ADDR=127.0.0.1:18791  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-stream-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18791}"   # 避开生产 8790 与 W4.4 脚本的 18790

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（场景：流式/注入/卡住/默认）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: PATH=\$TMP/bin:\$PATH SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：页面 W2.1 部件；无 innerHTML 等 sink；SSE delta/tool/usage/reply 族序"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w21-manual.XXXXXX)" || exit 1
SERVE_PID=""
SSE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
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

# ---- 2. 桩 seek（PATH 前置）+ 桩项目 ----
# 事件形状 = seek -p --json 的 jsonLine 契约（对照 executor_test.go 的
# web_stream 桩）；批次间留真实延迟——流式渲染靠它区分「逐事件到达」与
# 「跑完一次性收」。注入场景的载荷是模型吐 <script> 的模拟，只走
# stdout→SSE JSON→页面 textContent 这条线，页面源里从不出现。
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# W2.1 手测桩：按 -p 消息切场景吐 JSONL。非场景消息走默认两段应答。
set -u
MSG=""
while [ $# -gt 0 ]; do
  case "$1" in
    -p) MSG="${2:-}"; shift 2 ;;
    *) shift ;;
  esac
done
em() { printf '%s\n' "$1"; }
case "$MSG" in
  流式)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"turn_start","index":0}'
    em '{"type":"text_delta","delta":"Hel"}'
    sleep 0.8
    em '{"type":"text_delta","delta":"lo 世界。\n```go\nfunc ok() {}"}'
    em '{"type":"tool_start","id":"t1","name":"read","args":"{\"path\":\"x\"}"}'
    sleep 0.8
    em '{"type":"tool_delta","id":"t1","name":"read","delta":"partial"}'
    em '{"type":"tool_end","id":"t1","name":"read","result":"secret payload","bytes":14}'
    em '{"type":"reasoning_delta","delta":"inner monologue","reasoning":true}'
    em '{"type":"text_delta","delta":"\n```\n完结。"}'
    sleep 0.3
    em '{"type":"turn_end","index":0,"prompt_tokens":1000,"completion_tokens":200,"cache_hit_tokens":800,"tool_calls":1}'
    em '{"type":"agent_end","turns":1,"session_id":"SW21A"}'
    ;;
  注入)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"看这段："}'
    sleep 0.5
    em '{"type":"text_delta","delta":"<script>window.__xss=1</script>"}'
    em '{"type":"text_delta","delta":"<img src=x onerror=\"window.__xss2=1\">"}'
    sleep 0.5
    em '{"type":"text_delta","delta":" 上面应显示为纯文本。"}'
    em '{"type":"agent_end","turns":1,"session_id":"SW21B"}'
    ;;
  卡住)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"stuck"}'
    sleep 60
    ;;
  *)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"桩应答："}'
    em "{\"type\":\"text_delta\",\"delta\":\"$MSG\"}"
    em '{"type":"agent_end","turns":1,"session_id":"SW21D"}'
    ;;
esac
STUB
chmod +x "$TMP/bin/seek"
PROJ="$TMP/proj"

# ---- 3. 临时 config + 起 serve ----
printf '{"serve":{"web_addr":"%s"}}\n' "$ADDR" > "$TMP/config.json"
echo "== serve up =="
: > "$TMP/serve.log"
PATH="$TMP/bin:$PATH" SEEK_HOME="$TMP" "$TMP/seek" serve --frontend=web 2> "$TMP/serve.log" &
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

# ---- 4. curl 自检（服务端半边；UI 消费的同一条线）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF "$2" && echo yes || echo no; }

page=$(curl -s "$BASE")
ck "GET / 200+页面" "yes" "$(printf '%s' "$page" | grep -q 'id="form"' && echo yes || echo no)"
ck "页面含 W2.1 流式部件" "yes" "$(has "$page" "addEventListener('delta'")"
ck "页面含工具行折叠部件" "yes" "$(has "$page" "addEventListener('tool'")"
ck "XSS 静态：无 innerHTML" "no" "$(has "$page" "innerHTML")"
ck "XSS 静态：无 insertAdjacentHTML" "no" "$(has "$page" "insertAdjacentHTML")"
ck "XSS 静态：无 document.write" "no" "$(has "$page" "document.write")"

# SSE：订阅 curlconv 这个 conv，跑 /bind + 流式场景，验事件族与 §2.5 过滤。
: > "$TMP/sse.log"
curl -sN "$BASE/events?conv=curlconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5   # 等订阅落册（MVP 无回放：先订后发）
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d '{"text":"流式"}' > /dev/null
sleep 4     # 桩全程 ~2s + 收口
kill "$SSE_PID" 2>/dev/null
wait "$SSE_PID" 2>/dev/null   # 只等这一个：裸 wait 会连 serve 一起等成死等
SSE_PID=""

sse=$(cat "$TMP/sse.log")
ck "SSE：bind 应答（run-less reply）" "yes" "$(has "$sse" "已绑定项目")"
ck "SSE：delta 事件族" "yes" "$(printf '%s' "$sse" | grep -q '^event: delta$' && echo yes || echo no)"
ck "SSE：tool 事件族（start+end）" "2" "$(printf '%s' "$sse" | grep -c '^event: tool$')"
ck "SSE：usage 事件族" "yes" "$(printf '%s' "$sse" | grep -q '^event: usage$' && echo yes || echo no)"
ck "SSE：reply 收口带 run+session_id" "yes" "$(printf '%s' "$sse" | grep -q '"run":"' && printf '%s' "$sse" | grep -q '"session_id":"SW21A"' && echo yes || echo no)"
ck "SSE：§2.5 过滤 args/result 不下发" "no" "$(has "$sse" "secret payload")"
ck "SSE：§2.5 过滤 reasoning 不下发" "no" "$(has "$sse" "inner monologue")"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless（Chrome + node CDP）=="
  NODE_PID=""
  "$NODE_BIN" "$HERE/web-stream-headless.mjs" "$URL" "$PROJ" --chrome "$CHROME_BIN" &
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
  1. /bind $PROJ → 收「已绑定项目 …」；顶栏变「已绑定 …」
  2. 发「流式」：⚙ read … 工具行 + ▍光标部分文本逐段上屏；收口后
     整泡替换成终文本（\`\`\` 围栏成代码块），工具行消失
  3. 发「注入」：<script>/<img onerror> 显示为纯文本，无脚本执行
  4. 发「卡住」→ stuck 气泡出现后发 /cancel：气泡被 ack 收口替换，
     「run 已中断。」独立成条
  5. 断开网络/杀 serve 再起：EventSource 自动重连（W1.x 既有面零回归）

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
