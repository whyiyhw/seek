#!/usr/bin/env bash
#
# web-stop-manual.sh — c_5m3fm6 生成中停止键（dsh InputBar 中断对齐）的
# 手测/无头验收脚本。
#
# 验收口径（卡面）：
#   ① 生成中按钮态正确切换（发送键变停止键）；
#   ② 中断后半截消息保留且后续可继续发新消息（UI 收口：流状态复位）；
#   ③ 无生成时停止按钮不出现；
#   ④ web-gate 过、go test ./... 绿（不在本脚本内，收尾跑）。
#
# 干什么：临时 SEEK_HOME + 桩 seek（PATH 前置劫持）+ 桩项目目录，起
# `seek serve --frontend=web`；先跑服务端自检（curl：页面部件 / 无 HTML
# 解析 sink / SSE 中断线序——/cancel 后恰一条 run-bearing 收口 + 终态
# run-less），再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-stop-headless.mjs 无头跑全
#     部断言（①键态切换+点击发 /cancel+草稿不动 / ②半截保留+标注+后续
#     可发新消息 / ③空态恒发送键）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。桩场景按消息文本切换：慢速（吐半截
# 后挂 60s，等停止）/ 其它（默认快答，验「中断后可继续发」）。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → /bind <脚本打印的桩项目路径> → 收「已绑定项目」；
#      输入栏右键恒「发送」（③：无生成不出现停止）
#   2. 发「慢速」：首段文本逐段上屏时，发送键变「⏹ 停止」（红底）（①）
#   3. 在输入框打几个字（草稿），点「⏹ 停止」：草稿原样保留；半截气泡
#      保留、尾部「—— 已中断 ——」，随后「run 已中断。」独立成条（②）
#   4. 再发任意消息（如「继续」）：正常走流式并收口——中断后可继续发
#      （②）；收口后发送键回「发送」（③）
#
# Env:
#   ADDR=127.0.0.1:18798  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-stop-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18798}"   # 避开生产 8790 与 18790-18797（W2.1~W5 各卡）

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（场景：慢速/默认）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: PATH=\$TMP/bin:\$PATH SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：页面 c_5m3fm6 部件；无 innerHTML 等 sink；SSE /cancel 线序"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-stop-manual.XXXXXX)" || exit 1
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
# web_stream 桩）。「慢速」先吐三段（~0.4s 间隔）再挂 60s——留足窗口观察
# 停止键态与点击；被 /cancel 杀掉即本卡的「生成中」。批次间留真实延迟：
# 键态翻转跟首个 delta/tool 事件走，得能观察到「生成中」窗口。
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# c_5m3fm6 手测桩：按 -p 消息切场景吐 JSONL。非场景消息走默认快答。
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
  慢速)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"半截第一段。"}'
    sleep 0.4
    em '{"type":"text_delta","delta":"半截第二段。"}'
    sleep 0.4
    em '{"type":"text_delta","delta":"半截第三段。"}'
    sleep 2
    # 取消若未生效，2.8s 处会多出这段——自检以它的缺席证明杀停。
    em '{"type":"text_delta","delta":"这段不该出现。"}'
    sleep 60
    ;;
  *)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"桩应答："}'
    em "{\"type\":\"text_delta\",\"delta\":\"$MSG\"}"
    em '{"type":"agent_end","turns":1,"session_id":"SSTOP"}'
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
has() { # has <内容> <子串>：case 不建管道——grep -q 提前退出会把上游 printf 踩成 broken pipe（web-attach-manual.sh 同款坑）
  case "$1" in *"$2"*) echo yes ;; *) echo no ;; esac
}

page=$(curl -s "$BASE")
ck "GET / 200+页面" "yes" "$(printf '%s' "$page" | grep -q 'id="form"' && echo yes || echo no)"
ck "页面含停止键态部件（renderSend）" "yes" "$(has "$page" "renderSend()")"
ck "页面含点击分岔（click 层 preventDefault）" "yes" "$(has "$page" "send.addEventListener('click'")"
ck "页面含停止指令透传" "yes" "$(has "$page" "sendText('/cancel', false)")"
ck "页面含中断标注" "yes" "$(has "$page" "—— 已中断 ——")"
ck "XSS 静态：无 innerHTML" "no" "$(has "$page" "innerHTML")"
ck "XSS 静态：无 insertAdjacentHTML" "no" "$(has "$page" "insertAdjacentHTML")"
ck "XSS 静态：无 document.write" "no" "$(has "$page" "document.write")"

# SSE：订阅 curlconv，/bind + 慢速 + /cancel——中断线序（服务端已由单测
# TestWeb_StreamCancelClosesRun 钉死，这里验沙盒实例同一形状：/cancel 后
# 无 delta、恰一条带 run 的收口 + run-less 终态）。
: > "$TMP/sse.log"
curl -sN "$BASE/events?conv=curlconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5   # 等订阅落册（MVP 无回放：先订后发）
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d '{"text":"慢速"}' > /dev/null
sleep 1.5   # 桩已吐前两段，仍在流中
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d '{"text":"/cancel"}' > /dev/null
sleep 1.5   # ack + 终态
kill "$SSE_PID" 2>/dev/null
wait "$SSE_PID" 2>/dev/null   # 只等这一个：裸 wait 会连 serve 一起等成死等
SSE_PID=""

sse=$(cat "$TMP/sse.log")
ck "SSE：bind 应答（run-less reply）" "yes" "$(has "$sse" "已绑定项目")"
ck "SSE：慢速 delta 到达（流确实在跑）" "yes" "$(has "$sse" "半截第一段")"
ck "SSE：中断后无新 delta" "no" "$(has "$sse" "这段不该出现")"
# 中断线序：两条「已中断」默认帧（ack + 终态），其中恰一条带 run——
# 单测 TestWeb_StreamCancelClosesRun 钉的形状，这里验沙盒实例同形。
# awk 逐帧状态机：event: 行标记命名帧，紧随的 data: 不算默认帧。
counts=$(awk '/^event: /{named=1;next} /^data: /{ if(!named && /已中断/){n++; if(index($0,"\"run\":\"")>0) r++} named=0 } END{print n+0":"r+0}' "$TMP/sse.log")
ck "SSE：两条已中断收口（ack+终态）" "2:1" "$counts"

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
  "$NODE_BIN" "$HERE/web-stop-headless.mjs" "$URL" "$PROJ" --chrome "$CHROME_BIN" &
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
  1. /bind $PROJ → 收「已绑定项目 …」；发送键恒「发送」（③）
  2. 发「慢速」：文本逐段上屏时发送键变「⏹ 停止」红底（①）
  3. 输入框打个草稿，点「⏹ 停止」：草稿保留；半截气泡保留 + 尾部
     「—— 已中断 ——」；「run 已中断。」独立成条（②）
  4. 再发「继续」：正常流式收口（中断后可继续发，②）；键回「发送」（③）

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
