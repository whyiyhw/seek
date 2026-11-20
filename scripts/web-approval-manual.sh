#!/usr/bin/env bash
#
# web-approval-manual.sh — W5 卡 c_n37tnr（远程审批卡片）的手测/无头
# 验收脚本（feature-web-frontend §3.4 + §13.1）。卡面验收三项全走沙盒：
#   ① 假审批事件全流转：桩 seek 吐 approval_request → SSE approval 卡片
#      事件 → POST /rpc approval/decide → 决定回调 + resolved 回执 →
#      approval/list 不再列卡；
#   ② 操作幂等：重复 decide 读回同一决定 applied=false、决定不双发；
#   ③ 空态不打扰：无审批时 SSE 零 approval 事件、list 空、页面零痕迹
#      （角标 hidden / 卡栈 :empty 隐藏——页面形状由 Go 测试钉，这里钉
#      行为）。
#
# 干什么：临时 SEEK_HOME + 桩 seek（PATH 前置——executor 按名字找 seek）
# + 桩项目目录，起 `seek serve --frontend=web`；先跑 curl 自检（上面①②③
# 的服务端半边），再走浏览器无头验收（卡片弹出/角标计数/点击撤卡/防抖，
# scripts/web-approval-headless.mjs）；缺 Chrome/node 时打印人工清单。
#
# 桩契约即后续卡的子进程契约（webfrontend_approval.go 头注）：桩吐
# {"type":"approval_request","req_id":…,"kind":…,"summary":…,"command":…}
# 然后照常收尾——决定回注（stdin 写回）是后续卡的事，本卡验收的是
# 卡片流。全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → /bind <桩项目路径> → 顶栏无审批角标（空态零侵入）
#   2. 发「审批」：右下弹卡（描述+命令摘要+三按钮），顶栏角标 ⏳ 待审批 1
#   3. 点 ✅ 允许：按钮立即全禁用（防抖），卡片消失，角标隐没
#   4. 再发「审批」→ 点 ❌ 拒绝：同样撤卡
#   5. 重复快速点同一按钮：无第二次 POST（按钮已禁用），决定不双发
#
# Env:
#   ADDR=127.0.0.1:18793  监听地址（避开生产 8790 与 W2.1/W4.4 脚本）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-approval-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18793}"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（场景：审批/其它默认）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: PATH=\$TMP/bin:\$PATH SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：SSE approval 事件族 → decide → 幂等复读 → 空态零事件"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5ap-manual.XXXXXX)" || exit 1
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

# ---- 2. 桩 seek + 桩项目 ----
# 桩按 -p 消息切场景：「审批」吐一条 approval_request（req_id 带本桩进程
# PID，跨消息唯一——重复决定/复活卡的场景由幂等路径自己验）再照常收尾。
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# W5 c_n37tnr 手测桩：按 -p 消息切场景吐 JSONL。非场景消息走默认应答。
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
  审批)
    APID="ap-e2e-$$-$RANDOM"
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em "{\"type\":\"approval_request\",\"req_id\":\"$APID\",\"kind\":\"bash\",\"summary\":\"在桩项目里跑一次全量测试\",\"command\":\"go test ./... -count=1\"}"
    sleep 1
    em '{"type":"text_delta","delta":"审批请求已送出（桩不等待决定——回注是后续卡）。"}'
    em '{"type":"agent_end","turns":1,"session_id":"SAP1"}'
    ;;
  *)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"桩应答："}'
    em "{\"type\":\"text_delta\",\"delta\":\"$MSG\"}"
    em '{"type":"agent_end","turns":1,"session_id":"SAPD"}'
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
BASE="${URL%%\?*}"; BASE="${BASE%/}"
TOK="${URL#*token=}"

# ---- 4. curl 自检（①②③ 服务端半边）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF -- "$2" && echo yes || echo no; }
# needAll <haystack> <needle>…：一个应答对多个子串全中才 yes（单行输出——
# ck 的第三参只认一行）。has 的链式组合会一行一个 yes/no，凑不出单个判定。
needAll() {
  local hay="$1"; shift
  local n
  for n in "$@"; do
    printf '%s' "$hay" | grep -qF -- "$n" || { echo no; return; }
  done
  echo yes
}
rpcq() { # rpcq <method> <params> → 响应 JSON
  curl -s -X POST "$BASE/rpc?token=$TOK" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}

# ③ 空态先行：无审批时 SSE 零 approval 事件、list 空。
: > "$TMP/sse0.log"
curl -sN "$BASE/events?conv=emptyconv&token=$TOK" > "$TMP/sse0.log" &
SSE_PID=$!
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: emptyconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: emptyconv' \
  -H 'Content-Type: application/json' -d '{"text":"随便聊聊"}' > /dev/null
sleep 1.5
kill "$SSE_PID" 2>/dev/null; wait "$SSE_PID" 2>/dev/null; SSE_PID=""
sse0=$(cat "$TMP/sse0.log")
ck "③ 空态：普通消息流零 approval 事件" "0" "$(printf '%s' "$sse0" | grep -c '^event: approval$')"
ck "③ 空态：approval/list 空" "yes" "$(has "$(rpcq approval/list '{}')" '"approvals":[]')"

# ① 全流转：SSE 订阅 → 触发审批 → 卡片事件 → decide（订阅保持，回执要落
# 进同一条流）→ 回执 → 撤卡。② 幂等在回执之后原地复读。
: > "$TMP/sse.log"
curl -sN "$BASE/events?conv=curlconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d '{"text":"审批"}' > /dev/null

# 等卡片事件落地（桩 ~1s 出 approval_request），再抓 id 决定。
APID=""
for _ in $(seq 1 40); do
  APID=$(sed -n 's/^data: {"id":"\(ap-e2e-[^"]*\)".*/\1/p' "$TMP/sse.log" | head -1)
  [ -n "$APID" ] && break
  sleep 0.25
done
[ -n "$APID" ] || { kill "$SSE_PID" 2>/dev/null; echo "FAIL: 10s 内未见审批卡片事件"; cat "$TMP/sse.log"; exit 1; }
# ${APID} 花括号不可省：macOS bash 3.2 在 set -u 下把紧跟多字节字符的
# $APID 误读进变量名（web-stream-manual.sh 同款坑）。
echo "  （审批 id=${APID}）"

dec1=$(rpcq approval/decide "{\"id\":\"$APID\",\"decision\":\"y\"}")
ck "① decide 成功 applied=true" "yes" "$(needAll "$dec1" '"applied":true' '"state":"resolved"' '"decision":"y"')"
sleep 1     # 等回执帧写进同一条订阅流
kill "$SSE_PID" 2>/dev/null; wait "$SSE_PID" 2>/dev/null; SSE_PID=""
sse=$(cat "$TMP/sse.log")

ck "① 卡片事件（pending+载荷）" "yes" "$(needAll "$sse" '"state":"pending"' '"kind":"bash"' '"summary":"在桩项目里跑一次全量测试"' '"command":"go test ./... -count=1"')"
ck "① 卡片带 run id（键控回注的键源）" "yes" "$(printf '%s' "$sse" | grep -A1 '^event: approval$' | grep -q '"run":"' && echo yes || echo no)"
ck "① 回执事件（resolved，同一条流）" "yes" "$(needAll "$sse" '"state":"resolved"' "\"decision\":\"y\"")"

# ② 幂等：重复 decide（同决定/换决定）与未知 id / 坏决定。
dec2=$(rpcq approval/decide "{\"id\":\"$APID\",\"decision\":\"y\"}")
ck "② 重复 decide applied=false" "yes" "$(needAll "$dec2" '"applied":false')"
dec3=$(rpcq approval/decide "{\"id\":\"$APID\",\"decision\":\"n\"}")
ck "② 换决定复读：读回首个决定" "yes" "$(needAll "$dec3" '"applied":false' '"decision":"y"')"
decn=$(rpcq approval/decide '{"id":"nope","decision":"y"}')
ck "② 未知 id → -32602" "yes" "$(needAll "$decn" '"code":-32602')"
decm=$(rpcq approval/decide "{\"id\":\"$APID\",\"decision\":\"maybe\"}")
ck "② 坏决定 → -32602" "yes" "$(needAll "$decm" '"code":-32602')"
ck "① 撤卡后 list 不再列" "yes" "$(has "$(rpcq approval/list '{"conv":"curlconv"}')" '"approvals":[]')"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-approval-headless.mjs" "$URL" "$PROJ" --chrome "$CHROME_BIN" &
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
  1. /bind $PROJ → 顶栏无审批角标、右下无卡（空态零侵入）
  2. 发「审批」：右下弹卡（描述+命令摘要+✅/❌/♾ 三按钮），顶栏 ⏳ 待审批 1
  3. 点 ✅ 允许：按钮立即全禁用，卡片消失，角标隐没
  4. 再发「审批」→ 点 ❌ 拒绝：同样撤卡
  5. 狂点同一按钮：无第二次请求（按钮已禁用），决定不双发

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
