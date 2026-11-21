#!/usr/bin/env bash
#
# web-md-manual.sh — W5 markdown 渲染（卡 c_x2au4t）的手测/无头验收脚本。
# 验收（卡面）：①含 md 消息渲染正确、恶意 script/onclick 被转义；②流式
# 过程不闪烁。
#
# 干什么：临时 SEEK_HOME + 桩 seek（PATH 前置劫持，场景：渲染/注入/流式/
# 其它默认）+ 桩项目目录，起 `seek serve --frontend=web`；先跑服务端自检
# （curl：/static/markdown.js 资产面 + 页面无 HTML 解析 sink + SSE 事件族），
# 再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-md-headless.mjs 无头跑全部
#     断言（渲染形状/注入字面/流式不闪烁）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 输入 /bind <脚本打印的桩项目路径> 发送 → 收「已绑定项目」
#   2. 发「渲染」：收口后 ## 标题、列表、`行内码`、围栏代码块、可点链接
#      （外链新开页）各就位；[坏链](javascript:…) 显示为字面文本不可点
#   3. 发「注入」：<script>/<div onclick>/<a href=javascript:> 全部字面，
#      页面无脚本执行痕迹
#   4. 发「流式」：▍光标气泡逐步增长无闪烁跳变；收口整泡重渲成 md 形状
#
# Env:
#   ADDR=127.0.0.1:18795  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误；
#         3 无头条件缺且 stdin 非 TTY（无人值守车道：报环境能力缺，不假装绿）。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-md-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18795}"   # 避开生产 8790 与 W2.1/W4.4 的 18791/18790

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（场景：渲染/注入/流式/默认）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: PATH=\$TMP/bin:\$PATH SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：/static/markdown.js 资产面；页面+资产无解析 sink；SSE 族序"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5md-manual.XXXXXX)" || exit 1
SERVE_PID=""
SSE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then
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
# 事件形状 = seek -p --json 的 jsonLine 契约（web-stream-manual.sh 同款）。
# 「流式」delta 序列刻意只追加完整构造（标题/列表项/围栏整行进）——单调
# 增长是无头「不闪烁」断言的口径；「注入」载荷走 stdout→SSE→textContent
# 线，页面源里从不出现。
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# W5 markdown 手测桩：按 -p 消息切场景吐 JSONL。非场景消息走默认应答。
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
  渲染)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"## 标题二\n### 标题三\n\n- 无序甲\n- 无序乙\n\n1. 有序一\n2. 有序二\n\n"}'
    sleep 0.5
    em '{"type":"text_delta","delta":"正文 `code` 与 [外链](https://example.com/x?a=1) 与 [相对](/state) 与 [坏链](javascript:alert(1))。\n\n```go\nfunc md() {}\n```\n\n收束行。"}'
    em '{"type":"agent_end","turns":1,"session_id":"SW5MDA"}'
    ;;
  注入)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"<script>window.__xss=1</script>\n<div onclick=\"window.__xss2=1\">div</div>\n<a href=\"javascript:window.__xss3=1\">点我</a>\n[点我](javascript:void(0))\n"}'
    sleep 0.5
    em '{"type":"text_delta","delta":"上面应全部为纯文本。"}'
    em '{"type":"agent_end","turns":1,"session_id":"SW5MDB"}'
    ;;
  流式)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"流式正文第一段。"}'
    sleep 0.35
    em '{"type":"text_delta","delta":"\n第二段继续追加。"}'
    sleep 0.35
    em '{"type":"text_delta","delta":"\n## 流式标题"}'
    sleep 0.35
    em '{"type":"text_delta","delta":"\n- 流式条目"}'
    sleep 0.35
    em '{"type":"text_delta","delta":"\n```go"}'
    sleep 0.35
    em '{"type":"text_delta","delta":"\nrender(md)"}'
    sleep 0.35
    em '{"type":"text_delta","delta":"\n```"}'
    sleep 0.35
    em '{"type":"text_delta","delta":"\n流式完结。"}'
    sleep 0.3
    em '{"type":"agent_end","turns":1,"session_id":"SW5MDC"}'
    ;;
  *)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"桩应答："}'
    em "{\"type\":\"text_delta\",\"delta\":\"$MSG\"}"
    em '{"type":"agent_end","turns":1,"session_id":"SW5MDD"}'
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
ck "页面挂 static 渲染器" "yes" "$(has "$page" 'src="static/markdown.js"')"
ck "XSS 静态：页面无解析 sink（3 项）" "0" "$(printf '%s' "$page" | grep -cE 'innerHTML|insertAdjacentHTML|document\.write')"
asset_code=$(curl -s -o "$TMP/md.js" -w '%{http_code}' "$BASE/static/markdown.js")
ck "GET /static/markdown.js 200" "200" "$asset_code"
asset_ct=$(curl -sI "$BASE/static/markdown.js" | tr -d '\r' | sed -n 's/^Content-Type: //p')
ck "资产 Content-Type" "application/javascript; charset=utf-8" "$asset_ct"
asset_csp=$(curl -sI "$BASE/static/markdown.js" | tr -d '\r' | sed -n 's/^Content-Security-Policy: //p' | cut -c1-15)
ck "资产带 CSP 头" "default-src 'se" "$asset_csp"
asset=$(cat "$TMP/md.js")
ck "XSS 静态：资产无解析 sink（3 项）" "0" "$(printf '%s' "$asset" | grep -cE 'innerHTML|insertAdjacentHTML|document\.write')"
ck "资产含渲染器出口" "yes" "$(has "$asset" "window.seekMD")"
ck "资产含协议白名单" "yes" "$(has "$asset" "mailto:")"
ck "目录不可列（/static/ 拒答）" "404" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/static/")"
# -L 跟随 ServeMux 的洗牌重定向：无论中途是否 301，落点必须不是 200
ck "穿越不可达（/static/..%2F..%2Fetc 落点非 200）" "no" "$(curl -sL --path-as-is -o /dev/null -w '%{http_code}' "$BASE/static/..%2F..%2Fetc/passwd" | grep -qx 200 && echo yes || echo no)"

# SSE：订阅 mdconv，跑 /bind + 渲染场景，验事件族（传输层不因渲染器改动回归）。
: > "$TMP/sse.log"
curl -sN "$BASE/events?conv=mdconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5   # 等订阅落册（MVP 无回放：先订后发）
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: mdconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: mdconv' \
  -H 'Content-Type: application/json' -d '{"text":"渲染"}' > /dev/null
sleep 3     # 桩全程 ~1s + 收口
kill "$SSE_PID" 2>/dev/null
wait "$SSE_PID" 2>/dev/null
SSE_PID=""

sse=$(cat "$TMP/sse.log")
ck "SSE：bind 应答（run-less reply）" "yes" "$(has "$sse" "已绑定项目")"
ck "SSE：delta 事件族" "yes" "$(printf '%s' "$sse" | grep -q '^event: delta$' && echo yes || echo no)"
ck "SSE：reply 收口带 run+session_id" "yes" "$(printf '%s' "$sse" | grep -q '"run":"' && printf '%s' "$sse" | grep -q '"session_id":"SW5MDA"' && echo yes || echo no)"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-md-headless.mjs" "$URL" "$PROJ" --chrome "$CHROME_BIN" &
  NODE_PID=$!
  wait "$NODE_PID"; rc=$?
  NODE_PID=""
  [ "$rc" = 0 ] || exit 1
  echo "无头验收全绿。现场已清理（KEEP=1 可保留复跑）"
  exit 0
fi

# 无头条件缺 + 非 TTY = 无人值守车道：人工清单无处可走。read 在这里要么
# 挂到车道超时，要么 stdin 已关时读空即过——后者会给出无凭据的绿。退 3，
# 点名缺哪样能力（c_kwiajf 判据，web-sessions-manual.sh:221-230 同款；
# c_gqo32q 补此块后方移出 selftest 排除表）。
if [ ! -t 0 ]; then
  miss=""
  [ -x "$CHROME_BIN" ] || miss="Chrome（${CHROME_BIN}）"
  [ -n "$NODE_BIN" ] || miss="${miss:+${miss} + }node"
  echo "FAIL: 无头验收条件缺（${miss}）且 stdin 非 TTY——无人值守没有人工清单可走。装上缺的组件或 CHROME=<路径> 重跑。" >&2
  exit 3
fi

cat <<EOF

== 无头条件缺（Chrome/node）——人工手测清单（浏览器）==
  打开：$URL
  1. /bind $PROJ → 收「已绑定项目 …」
  2. 发「渲染」：收口后 ## 标题、- 列表、\`行内码\`、go 围栏代码块、
     [外链](https://example.com/x?a=1) 可点新开页；[坏链](javascript:…) 字面不可点
  3. 发「注入」：<script>/<div onclick>/javascript: 链全部字面纯文本，
     无脚本执行痕迹
  4. 发「流式」：▍气泡逐步增长无跳变闪烁；收口整泡重渲成 md 形状

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
