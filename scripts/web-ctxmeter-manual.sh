#!/usr/bin/env bash
#
# web-ctxmeter-manual.sh — W5 卡 c_oyp2qf 顶栏 ContextMeter 的手测/无头
# 验收脚本（feature-web-frontend §13.1 c_oyp2qf 行；对齐 dsh ContextMeter
# 语义：窗口占用百分比 + 色阶预警）。
#
# 干什么：临时 SEEK_HOME + 桩 seek（PATH 前置劫持，同 W2.1 脚本手法）+
# 桩项目目录，起 `seek serve --frontend=web`；先跑服务端自检（curl：页面
# 部件；SSE usage 帧带 model/lane/ctx_limit 三件且两车道样例判定正确——
# deepseek-flash=DeepSeek 直连 / deepseek-v4.1-flash=OpenCode 网关，
# 后端判定源=internal/modelcap LaneOf + internal/budget Limit），再走
# 浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-ctxmeter-headless.mjs 无头跑
#     全部断言（余量条随 usage 更新+色阶、两车道显示、杀 serve 演练
#     灯黄→提示条→手动重连→自动恢复转绿→续流）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → /bind <桩项目路径> → 发「车道直连」：顶栏出现
#      「deepseek-flash · DeepSeek 直连」+ 65%·余350K 的黄档进度条
#   2. 发「车道网关」：车道名切到「OpenCode 网关」（同一模型家族、两条
#      车道的两个 wire id）
#   3. 发「吃紧」→ 85% 红档；发「宽裕」→ 11% 回绿档
#   4. kill serve：右上灯●转黄 + 顶栏下出现「SSE 已断开」提示条与
#      「手动重连」按钮；点手动重连（服务端仍停时保持黄）
#   5. 重启 serve：≤4s 灯自动转绿、提示条消失；再发「宽裕」应答照常
#      到达、余量条刷新——断线前后的流无缝续上
#
# Env:
#   ADDR=127.0.0.1:18796  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-ctxmeter-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18796}"   # 避开生产 8790 与 18790–18795 的既有脚本

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（场景：车道直连/车道网关/吃紧/宽裕）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}，起 serve 抓 banner"
  echo "DRY_RUN: curl 自检：页面 ContextMeter/连接灯部件；SSE usage 帧 model/lane/ctx_limit 两车道判定"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言（含 kill/重启 serve 演练）；缺 → 人工清单"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5c-manual.XXXXXX)" || exit 1
SERVE_PID=""
SSE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  # 演练半途重启的第二个 serve：headless 起的，pid 落在 pidfile
  if [ -f "$TMP/serve2.pid" ]; then
    kill "$(cat "$TMP/serve2.pid")" 2>/dev/null
  fi
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
# 两车道样例 = 同一模型家族的两个 wire id（DeepSeek V4.1 Flash：直连
# deepseek-flash / 网关 deepseek-v4.1-flash）。usage 数字造在色阶档位上：
# 650K/1M=65%（黄）、850K/1M=85%（红）、110K/1M=11%（绿）。
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# W5 c_oyp2qf 手测桩：按 -p 消息切车道/档位场景吐 JSONL。
set -u
MSG=""
while [ $# -gt 0 ]; do
  case "$1" in
    -p) MSG="${2:-}"; shift 2 ;;
    *) shift ;;
  esac
done
em() { printf '%s\n' "$1"; }
usage() { # usage <model> <prompt> <completion> <cache> <sid>
  em "{\"type\":\"agent_start\",\"model\":\"$1\"}"
  em '{"type":"turn_start","index":0}'
  em '{"type":"text_delta","delta":"done"}'
  em "{\"type\":\"turn_end\",\"index\":0,\"prompt_tokens\":$2,\"completion_tokens\":$3,\"cache_hit_tokens\":$4,\"tool_calls\":0}"
  em "{\"type\":\"agent_end\",\"turns\":1,\"session_id\":\"$5\"}"
}
case "$MSG" in
  车道直连) usage deepseek-flash 620000 30000 100000 SW5CA ;;
  车道网关) usage deepseek-v4.1-flash 620000 30000 100000 SW5CB ;;
  吃紧)    usage deepseek-flash 800000 50000 120000 SW5CC ;;
  宽裕)    usage deepseek-flash 100000 10000 20000 SW5CD ;;
  *)       usage deepseek-flash 1000 100 0 SW5CZ ;;
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

# ---- 4. curl 自检（服务端半边；UI 消费的同一条线）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF "$2" && echo yes || echo no; }

page=$(curl -s "$BASE")
ck "页面含 ContextMeter 部件" "yes" "$(has "$page" 'renderCtx')"
ck "页面含连接灯/提示条/手动重连" "yes" "$(has "$page" 'id="reconn"')"
ck "页面色阶阈值=budget 口径(0.6/0.75)" "yes" "$(has "$page" "frac >= 0.75 ? 'crit' : frac >= 0.6 ? 'warn' : ''")"
ck "旧 onerror 占位绑定行已退役" "no" "$(has "$page" "setStatus('SSE 断开")"

# SSE：两车道样例各跑一枪，验 usage 帧三件（model/lane/ctx_limit）与后端
# 判定一致（modelcap.LaneOf / budget.Limit——同源，见 ctxmeter Go 测试）。
: > "$TMP/sse.log"
curl -sN "$BASE/events?conv=curlconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d '{"text":"车道直连"}' > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d '{"text":"车道网关"}' > /dev/null
sleep 2
kill "$SSE_PID" 2>/dev/null; SSE_PID=""

sse="$TMP/sse.log"
lane1=$(grep -o '"lane":"[^"]*"' "$sse" | head -1)
ck "直连样例 lane" '"lane":"DeepSeek 直连"' "$lane1"
mod1=$(grep -o '"model":"deepseek-flash"' "$sse" | head -1)
ck "直连样例 model" '"model":"deepseek-flash"' "$mod1"
lane2=$(grep -o '"lane":"[^"]*"' "$sse" | sed -n 2p)
ck "网关样例 lane" '"lane":"OpenCode 网关"' "$lane2"
mod2=$(grep -o '"model":"deepseek-v4.1-flash"' "$sse" | head -1)
ck "网关样例 model" '"model":"deepseek-v4.1-flash"' "$mod2"
lim=$(grep -o '"ctx_limit":[0-9]*' "$sse" | head -1)
ck "ctx_limit=1M（budget 同源）" '"ctx_limit":1000000' "$lim"

[ "$fail" = 0 ] || { echo "自检未过——停在这里（浏览器演练不再起）"; exit 1; }

# ---- 5. 浏览器验收 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
if [ -x "$CHROME_BIN" ] && command -v node > /dev/null 2>&1 \
   && node -e 'process.exit(typeof WebSocket==="function"?0:1)' 2>/dev/null; then
  echo "== headless drill =="
  KILL_CMD="kill $SERVE_PID 2>/dev/null"
  START_CMD="PATH='$TMP/bin:$PATH' SEEK_HOME='$TMP' '$TMP/seek' serve --frontend=web 2>>'$TMP/serve.log' & echo \$! > '$TMP/serve2.pid'"
  node "$HERE/web-ctxmeter-headless.mjs" "$URL" "$PROJ" \
    --kill "$KILL_CMD" --start "$START_CMD" \
    ${CHROME:+--chrome "$CHROME"} &
  NODE_PID=$!
  wait "$NODE_PID"; rc=$?
  NODE_PID=""
  [ "$rc" = 0 ] || exit 1
  echo "全部断言通过（W5 c_oyp2qf 无头验收）"
  exit 0
fi

echo "== 人工清单（Chrome/node 不可用）=="
cat <<'LIST'
  1. 打开上面打印的 URL → /bind <桩项目路径> → 发「车道直连」：顶栏出现
     「deepseek-flash · DeepSeek 直连」+ 黄档 65% · 余350K
  2. 发「车道网关」：车道名切「OpenCode 网关」
  3. 发「吃紧」→ 85% 红档；发「宽裕」→ 11% 回绿档
  4. kill serve（KEEP=1 时另开终端 kill <printed pid>）：灯●转黄 + 「SSE
     已断开」提示条 + 手动重连按钮；点它（保持黄）
  5. 重启 serve：灯自动转绿、提示条消失；发「宽裕」应答到达、余量条刷新
LIST
read -r _?
exit 0
