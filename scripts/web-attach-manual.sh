#!/usr/bin/env bash
#
# web-attach-manual.sh — W5 附件上传（卡 c_56yrcj）的手测/无头验收脚本。
#
# 验收口径（卡面）：
#   ① 三入口（点击选择 / 拖放 / 粘贴）在本地 serve 沙盒走通；
#   ② 超限/坏文件的提示正确；
#   ③ 既有资产面零回归（CSRF 闸只卡 cookie、gzip 保 Flusher、SSE 不压缩）；
#   ④ web-gate 过、go test ./... 绿。
#
# 干什么：临时 SEEK_HOME（-- 不碰真实 ~/.seek）+ 桩 seek（PATH 前置劫持，
# 把每次收到的 -p 消息写进 argv 日志，供无头脚本断言「@路径真的到了 CLI」）
# + 桩项目目录，起 `seek serve --frontend=web`；先跑服务端自检（curl：
# /attach 鉴权/上限/魔数/巨型名、页面部件、CSP 的 img-src、SSE 不压缩），
# 再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-attach-headless.mjs 无头跑三入口；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰生产 8790，不写真 ~/.seek。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → /bind <脚本打印的桩项目路径>
#   2. 📎 选一张 png → 托盘出现条目、进度到「已就绪」→ 发送 → 气泡里
#      出现缩略图；服务端日志/桩 argv 里那条消息带 @…png
#   3. 把 png 从桌面拖进页面 → 同上（落点虚线提示出现又消失）
#   4. 截图后 Ctrl/⌘+V → 同上
#   5. 选一个 .txt（或把脚本改名成 .png）→ 托盘标红并给文案，发送前提示
#      「N 个附件未就绪」，那条路径不会进正文
#
# Env:
#   ADDR=127.0.0.1:18797  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-attach-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18797}"   # 避开生产 8790 与 W2.1/W4.4/W5md 的 1879x

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（把 -p 正文写进 \$TMP/argv.log）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: PATH=\$TMP/bin:\$PATH SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：/attach 401/403/201/405/413/415；页面部件；CSP img-src；SSE 不压缩"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头三入口断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5att-manual.XXXXXX)" || exit 1
SERVE_PID=""
SSE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then
    # ${TMP} 花括号不可省：macOS bash 3.2 在 set -u 下把紧跟多字节字符的
    # $TMP 误读进变量名（docs/pitfalls.md 有这条）。
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
# 桩只做两件事：把 argv 里的 -p 正文追加进 argv 日志（无头断言「附件路径
# 走到了 CLI」的唯一证据），吐一份最小 jsonLine 应答。它不做能力判定——真
# 判定在真 seek 的 visionRouter/fileroute 里，这里验的是协议有没有接上。
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
ARGV_LOG="$TMP/argv.log"
: > "$ARGV_LOG"
# 日志路径在生成期展开（heredoc 未加引号 → $TMP 就地替换；\$MSG/\$# 保持
# 到运行期），桩因此不需要从环境里找路径。
cat > "$TMP/bin/seek" <<STUB
#!/usr/bin/env bash
set -u
MSG=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -p) MSG="\${2:-}"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "\$MSG" >> "$ARGV_LOG"
printf '%s\n' '{"type":"agent_start","model":"deepseek-chat"}'
if [ "\${MSG#/bind}" != "\$MSG" ]; then
  printf '%s\n' '{"type":"text_delta","delta":"已绑定项目（桩）"}'
else
  printf '%s\n' '{"type":"text_delta","delta":"桩应答收到。"}'
fi
printf '%s\n' '{"type":"agent_end","turns":1,"session_id":"SW5ATT"}'
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

# ---- 4. 服务端自检（curl；UI 消费的同一条线）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF "$2" && echo yes || echo no; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

# 一张真的 1×1 PNG（base64 解码）。用 printf 拼二进制，避免依赖外部工具。
PNG_B64='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='
printf '%s' "$PNG_B64" | base64 -d > "$TMP/tiny.png" 2>/dev/null \
  || printf '%s' "$PNG_B64" | base64 --decode > "$TMP/tiny.png"
ck "/attach 无凭证 401" "401" "$(code -X POST --data-binary @"$TMP/tiny.png" "$BASE/attach?name=a.png")"
ck "/attach cookie 无 Origin 403（CSRF 闸只卡 cookie）" "403" \
  "$(code -X POST -H "Cookie: seek_web_token=$TOK" --data-binary @"$TMP/tiny.png" "$BASE/attach?name=a.png")"
ck "/attach 合法 png 201" "201" \
  "$(code -X POST -H "X-Seek-Conversation: curlconv" --data-binary @"$TMP/tiny.png" "$BASE/attach?token=$TOK&name=a.png")"
ck "/attach GET 405" "405" "$(code "$BASE/attach?token=$TOK&name=a.png")"
printf 'not a pdf' > "$TMP/fake.pdf"
ck "/attach 改名坏文件 415" "415" \
  "$(code -X POST --data-binary @"$TMP/fake.pdf" "$BASE/attach?token=$TOK&name=fake.pdf")"
printf 'hello' > "$TMP/notes.txt"
ck "/attach 不支持类型 415" "415" \
  "$(code -X POST --data-binary @"$TMP/notes.txt" "$BASE/attach?token=$TOK&name=notes.txt")"
ck "/attach 空 body 400" "400" \
  "$(code -X POST --data-binary '' "$BASE/attach?token=$TOK&name=empty.png")"
# 超限 413 由单测覆盖（TestWeb_AttachSizeCaps 把 assets.MaxAssetBytes 调低
# 走同一个闸）——沙盒里没必要为一条文案造 33 MiB 的上传。
# 页面部件（用变量收集，避免 grep -q 提前退出把 printf 踩成 broken pipe）
page=$(curl -s "$BASE")
ui=yes
case "$page" in *'id="attach-btn"'*) ;; *) ui=no ;; esac
case "$page" in *"addEventListener('paste'"*) ;; *) ui=no ;; esac
case "$page" in *'dragHasFiles'*) ;; *) ui=no ;; esac
ck "页面含附件三入口部件" "yes" "$ui"
sinks=$(printf '%s' "$page" | grep -cE 'innerHTML|insertAdjacentHTML|document\.write')
ck "页面无 HTML 解析 sink" "0" "$sinks"
csp=$(curl -sI "$BASE/" | tr -d '\r' | sed -n 's/^Content-Security-Policy: //p')
ck "CSP 仍是 'self' 基线" "default-src 'self'" "$(printf '%s' "$csp" | cut -d';' -f1)"
ck "CSP 放行 img-src data:（缩略图预览）" "yes" "$(printf '%s' "$csp" | grep -q "img-src 'self' data:" && echo yes || echo no)"

# SSE 传输零回归：不压缩、逐帧送达（gzip 保 Flusher 那条坑的对偶）。
: > "$TMP/sse.log"
curl -sN -D "$TMP/sse.hdr" "$BASE/events?conv=attconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: attconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 1.5
kill "$SSE_PID" 2>/dev/null
wait "$SSE_PID" 2>/dev/null
SSE_PID=""
sse=$(cat "$TMP/sse.log")
ck "SSE：bind 应答到达（逐帧未压缩）" "yes" "$(has "$sse" "已绑定项目")"
ck "SSE：响应头无 Content-Encoding（text/event-stream 永不压缩）" "0" \
  "$(tr -d '\r' < "$TMP/sse.hdr" | grep -ci '^Content-Encoding:')"
ck "SSE：Content-Type 是 text/event-stream" "yes" \
  "$(tr -d '\r' < "$TMP/sse.hdr" | grep -qi '^Content-Type: text/event-stream' && echo yes || echo no)"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-attach-headless.mjs" "$URL" "$PROJ" "$ARGV_LOG" --chrome "$CHROME_BIN" &
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
  1. /bind $PROJ → 收「已绑定项目 …」
  2. 📎 选一张 png → 托盘出现条目、进度到「已就绪」；点发送 → 气泡里
     出现缩略图（服务端日志里那条消息带 @…png）
  3. 从桌面把 png 拖进页面 → 落点虚线提示出现、松手后消失，同上
  4. 截图后 Ctrl/⌘+V → 同上
  5. 选一个 .txt（或把脚本改名成 .png）→ 托盘标红并给文案，发送前提示
     「N 个附件未就绪」，且那条路径不进正文

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
