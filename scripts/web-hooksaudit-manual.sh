#!/usr/bin/env bash
#
# web-hooksaudit-manual.sh — W5 卡 c_xz2bey hooks 实时审计视图的手测/无头
# 验收脚本（feature-web-frontend §13.1 c_xz2bey 行；收口 inspect-rpc
# §6 #6/#7：/events 增 hooks topic + 审计面板）。
#
# 干什么：临时 SEEK_HOME（审计日志就在里面）+ 桩 seek + 桩项目，起
# `seek serve --frontend=web`；先跑服务端自检（curl：页面部件；SSE
# hooks topic 逐帧 verbatim + retry:3000 头 + 15s 心跳帧（§12 开放问题的
# 不回归证据，回环口径）；未知 topic 报错点名双 topic；/rpc
# hooks/audit/tail 的 follow:true 仍 -32602 但指路 /events?topic=hooks），
# 再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-hooksaudit-headless.mjs 无头
#     跑全部断言（快照+实时滚动追加、暂停/恢复、按 hook 名过滤、
#     杀 serve 断线→重启重连补拉不丢行）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。tailscale --tcp 转发下的 SSE 心跳
# 复核属真网动作，留主会话（脚本内注释有 curl 命令）。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 顶栏「🪝 审计」→ 面板出已落盘的审计行（快照）
#   2. 往审计日志追加一行（或跑一把带 hook 的 seek）→ 面板底部滚出
#      新行（无需刷新）
#   3. 「⏸ 暂停」→ 再追加两行 → 画面冻结、计数出现「2 新行」；
#      「▶ 恢复」→ 补齐到最新
#   4. 过滤框输 hook 名 → 只剩匹配行；清空恢复
#   5. kill serve → 追加两行 → 重启 serve → ≤5s 面板自动补上断线期间
#      的行（重连补拉）
#
# Env:
#   ADDR=127.0.0.1:18798  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-hooksaudit-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18798}"   # 避开生产 8790 与 18790–18797 的既有脚本

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/hooks-audit.jsonl 预置 3 行 + 40 行垫高（滚动用）"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}，起 serve 抓 banner"
  echo "DRY_RUN: curl 自检：页面部件；SSE verbatim 帧/retry 头/15s 心跳；未知 topic 双名；follow:true 指路"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言（滚动/暂停/过滤/断线补拉）；缺 → 人工清单"
  echo "DRY_RUN: tailscale --tcp 复核（留主会话）：tailscale serve --tcp=$ADDR 后"
  echo "  curl -sN \"http://<tailnet-ip>:$ADDR/events?topic=hooks&token=<tok>\" 16s 内应见 ': ping'"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5hk-manual.XXXXXX)" || exit 1
SERVE_PID=""
SSE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2> /dev/null
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2> /dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2> /dev/null && wait "$SERVE_PID" 2> /dev/null
  if [ -f "$TMP/serve2.pid" ]; then
    kill "$(cat "$TMP/serve2.pid")" 2> /dev/null
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

# ---- 2. 桩 seek（PATH 前置；本卡不发消息，桩只为 serve 的 exec 探活兜底）----
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# W5 c_xz2bey 手测桩：无场景分支——审计行由脚本直写审计日志。
printf '%s\n' '{"type":"agent_start","model":"deepseek-chat"}'
printf '%s\n' '{"type":"agent_end","turns":0,"session_id":"W5HK0"}'
STUB
chmod +x "$TMP/bin/seek"

# ---- 3. 审计日志预置：3 行可辨识历史 + 40 行垫高（保证面板可滚动）----
AUDIT="$TMP/hooks-audit.jsonl"
echo "== seed audit =="
: > "$AUDIT"
i=0
while [ $i -lt 40 ]; do
  printf '%s\n' "{\"ts\":\"2026-11-20T08:%02d:00Z\",\"event\":\"post_tool_use\",\"hook\":\"fmt\",\"tool\":\"bash\",\"session_id\":\"pad-$i\",\"duration_ms\":$((i+1)),\"exit_code\":0}" "$i" >> "$AUDIT"
  i=$((i+1))
done
printf '%s\n' '{"ts":"2026-11-20T09:00:00Z","event":"post_tool_use","hook":"fmt","tool":"bash","session_id":"s1","duration_ms":12,"exit_code":0}' >> "$AUDIT"
printf '%s\n' '{"ts":"2026-11-20T09:00:05Z","event":"pre_tool_use","hook":"lint","tool":"edit","session_id":"s1","duration_ms":3,"exit_code":1,"denied":true,"reason":"blocked"}' >> "$AUDIT"
printf '%s\n' '{"ts":"2026-11-20T09:00:09Z","event":"pre_prompt_inject","hook":"ctx","session_id":"s2","duration_ms":1,"exit_code":0,"injected_bytes":99}' >> "$AUDIT"
PROJ="$TMP/proj"

# ---- 4. 临时 config + 起 serve ----
printf '{"serve":{"web_addr":"%s"}}\n' "$ADDR" > "$TMP/config.json"
echo "== serve up =="
: > "$TMP/serve.log"
PATH="$TMP/bin:$PATH" SEEK_HOME="$TMP" "$TMP/seek" serve --frontend=web 2> "$TMP/serve.log" &
SERVE_PID=$!
URL=""
for _ in $(seq 1 50); do
  URL=$(sed -n 's/^web: up: //p' "$TMP/serve.log" | head -1)
  [ -n "$URL" ] && break
  if ! kill -0 "$SERVE_PID" 2> /dev/null; then
    echo "FAIL: serve 提前退出："; cat "$TMP/serve.log"; exit 1
  fi
  sleep 0.2
done
[ -n "$URL" ] || { echo "FAIL: 10s 内未见 banner"; exit 1; }
echo "$URL"
BASE="${URL%%\?*}"; BASE="${BASE%/}"
TOK="${URL#*token=}"

# ---- 5. curl 自检（服务端半边；UI 消费的同一条线）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" 2>/dev/null | grep -qF "$2" && echo yes || echo no; }

page=$(curl -s "$BASE")
ck "页面含审计面板部件(hk-log/hk-filter/hk-pause)" "yesyesyes" "$(has "$page" 'id="hk-log"')$(has "$page" 'id="hk-filter"')$(has "$page" 'id="hk-pause"')"
ck "页面含 hooks topic 订阅" "yes" "$(has "$page" 'topic=hooks')"

# 未知 topic 报错点名两个合法 topic（W3.2 文案随本卡更新；JSON 转义的
# 引号不进断言——两个名字在即够）。
err=$(curl -s "$BASE/events?token=$TOK&topic=weather")
ck "未知 topic 400 报双名" "yesyes" "$(has "$err" 'subagents')$(has "$err" 'hooks')"

# follow:true 仍 -32602，但指路已交付的 SSE topic（不再是 future work）。
fol=$(curl -s -X POST "$BASE/rpc?token=$TOK" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"hooks/audit/tail","params":{"follow":true}}')
ck "follow:true -32602 指路 topic" "yes" "$(printf '%s' "$fol" | grep -q '\-32602' && printf '%s' "$fol" | grep -q 'events?topic=hooks' && echo yes || echo no)"

# SSE：头三样（Content-Type / X-Accel-Buffering / 首行 retry:3000）+
# 追加一行 → verbatim 帧。心跳单独等 16s（§12 不回归证据，回环口径）。
: > "$TMP/sse.log"
curl -sN "$BASE/events?conv=curlconv&token=$TOK&topic=hooks" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5
LIVE='{"ts":"2026-11-20T09:00:20Z","event":"post_tool_use","hook":"fmt","tool":"bash","session_id":"live1","duration_ms":7,"exit_code":0}'
printf '%s\n' "$LIVE" >> "$AUDIT"
sleep 2
sse="$TMP/sse.log"
ck "SSE 头 Content-Type" "yes" "$(curl -sI "$BASE/events?token=$TOK&topic=hooks" | grep -qi 'content-type: text/event-stream' && echo yes || echo no)"
ck "SSE 头 X-Accel-Buffering:no" "yes" "$(curl -sI "$BASE/events?token=$TOK&topic=hooks" | grep -qi 'x-accel-buffering: no' && echo yes || echo no)"
ck "SSE 首行 retry:3000" "yes" "$(head -1 "$sse" | grep -q '^retry: 3000$' && echo yes || echo no)"
ck "SSE hooks 帧逐字 verbatim" "yes" "$(grep -qF "event: hooks
data: $LIVE" "$sse" && echo yes || echo no)"
ck "SSE 历史不重放（无 pad-39 之前锚点行）" "no" "$(grep -q '"hook":"ctx","session_id":"s2"' "$sse" && echo yes || echo no)"
kill "$SSE_PID" 2> /dev/null; SSE_PID=""

echo "  ..  等 15s 心跳帧（SSE beat，§12 不回归的回环证据）…"
: > "$TMP/sse2.log"
curl -sN "$BASE/events?conv=curlconv&token=$TOK&topic=hooks" > "$TMP/sse2.log" &
SSE_PID=$!
sleep 17
kill "$SSE_PID" 2> /dev/null; SSE_PID=""
ck "SSE 15s 心跳帧(: ping)" "yes" "$(grep -q '^: ping$' "$TMP/sse2.log" && echo yes || echo no)"

[ "$fail" = 0 ] || { echo "自检未过——停在这里（浏览器演练不再起）"; exit 1; }

# ---- 6. 浏览器验收 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
if [ -x "$CHROME_BIN" ] && command -v node > /dev/null 2>&1 \
   && node -e 'process.exit(typeof WebSocket==="function"?0:1)' 2> /dev/null; then
  echo "== headless drill =="
  KILL_CMD="kill $SERVE_PID 2>/dev/null"
  START_CMD="PATH='$TMP/bin:$PATH' SEEK_HOME='$TMP' '$TMP/seek' serve --frontend=web 2>>'$TMP/serve.log' & echo \$! > '$TMP/serve2.pid'"
  node "$HERE/web-hooksaudit-headless.mjs" "$URL" "$AUDIT" \
    --kill "$KILL_CMD" --start "$START_CMD" \
    ${CHROME:+--chrome "$CHROME"} &
  NODE_PID=$!
  wait "$NODE_PID"; rc=$?
  NODE_PID=""
  [ "$rc" = 0 ] || exit 1
  echo "全部断言通过（W5 c_xz2bey 无头验收）"
  exit 0
fi

echo "== 人工清单（Chrome 或 node≥22 不可用）=="
cat <<'LIST'
1. 浏览器打开上面的 URL → 顶栏「🪝 审计」：面板应显示 43 行快照
   （40 垫高 + fmt/lint/ctx 各一），贴底。
2. 等 1–2 秒后手动追加一行（命令在下）→ 面板底部滚出新行，无需刷新。
3. 「⏸ 暂停」→ 再追加两行 → 画面冻结，右上计数出现「2 新行」；
   「▶ 恢复」→ 立即补齐到最新并贴底。
4. 过滤框输「lint」→ 只剩 lint 行；清空 → 全量回来。
5. 杀 serve（kill）→ 追加两行 → 重启 serve → ≤5s 面板自动补上断线期间
   的两行（EventSource 自动重连 + 重开补拉）。
追加行示例：
  printf '%s\n' '{"ts":"2026-11-20T10:00:00Z","event":"post_tool_use","hook":"fmt","tool":"bash","session_id":"manual","duration_ms":5,"exit_code":0}' >> AUDIT_PATH
LIST
echo "AUDIT_PATH=$AUDIT"
read -r -p "按清单走完后按 Enter…"
echo "人工清单走完（W5 c_xz2bey）"
exit 0
