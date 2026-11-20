#!/usr/bin/env bash
#
# web-tree-manual.sh — c_ux7lvk 工具调用树（dsh 对齐）的手测/无头验收脚本。
#
# 干什么：临时 SEEK_HOME + 桩 seek（PATH 前置劫持）+ 会话夹具，起
# `seek serve --frontend=web`；先跑服务端自检（curl：页面部件 / 无 HTML
# 解析 sink / SSE tool 事件族不变 / /rpc session/messages 契约），再走
# 浏览器验收（scripts/web-tree-headless.mjs）：
#   - 流式面：调用树行（名称+耗时）+ 区间包含嵌套缩进 + ✘ 态 + 收口替换零回归；
#   - 会话面：树行展开/收起、长结果截断（END_MARK 不进 DOM）+ 复制全文
#     （剪贴板拿得到 END_MARK）、孤儿结果独立成泡、XSS 字面渲染；
#   - 兜底：毒消息（getter 抛错）→ 「（消息渲染失败）」占位 + 后续消息
#     恢复，页面不白屏。
# 全程不碰真实 ~/.seek 与生产 8790。桩场景按消息文本切换：树 / 其它
# （默认应答）。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → /bind <桩项目路径> → 发「树」：
#      ⚙ read 行缩进在 ⚙ grep 行之下（嵌套），行尾耗时跳动，收口冻结；
#      ✘ bash 行变警示色；reply 到达后整泡替换（工具行消失）
#   2. 📚 会话 → 点开夹具会话：三个调用树行；点 read 行展开——参数全文、
#      结果摘要截断（尾部提示），「复制全文」后粘贴可得含 END_MARK 的整串；
#      再点行收起
#   3. 孤儿 tool 消息独立成泡；xss 行的 <script> 字面显示、不执行
#   4. 控制台执行 appendMsg({get tool_calls(){throw 0}}) → 出「（消息渲染
#      失败）」占位，页面仍可用
#
# Env:
#   ADDR=127.0.0.1:18793  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-tree-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18793}"   # 避开生产 8790 与 W2.1/W4.4 脚本的 18790-92
TREE_SID="20261120-100000-ux7lvktreefx01"
TREE_PORT=18794                   # CDP 调试口（无头脚本参数默认同值）

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（场景：树/默认）+ \$TMP/proj 桩项目 + \$TMP/sessions/$TREE_SID.jsonl 夹具"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: PATH=\$TMP/bin:\$PATH SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：页面调用树部件；无 innerHTML 等 sink；SSE tool×6；/rpc messages 契约"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → scripts/web-tree-headless.mjs；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-ux7lvk-tree.XXXXXX)" || exit 1
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
# 事件形状 = seek -p --json 的 jsonLine 契约。「树」场景造区间嵌套：
# grep 先 start，read 趁其在途 start（→ 缩进一层），1.2s 后双双 end
# （grep 耗时 ≈1.4s、read ≈1.2s——耗时断言的实数据），bash 再 start/end
# 带 error（✘ 行）。收口终文本「树完成。」（reply 整泡替换零回归用）。
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# c_ux7lvk 调用树手测桩：按 -p 消息切场景吐 JSONL。非场景消息走默认应答。
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
  树)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"turn_start","index":0}'
    em '{"type":"text_delta","delta":"树测试开始。"}'
    em '{"type":"tool_start","id":"t1","name":"grep","args":"{\"q\":\"foo\"}"}'
    sleep 0.2
    em '{"type":"tool_start","id":"t2","name":"read","args":"{\"path\":\"a.go\"}"}'
    sleep 1.2
    em '{"type":"tool_end","id":"t2","name":"read","result":"ok","bytes":2}'
    em '{"type":"tool_end","id":"t1","name":"grep","result":"2 matches","bytes":9}'
    em '{"type":"tool_start","id":"t3","name":"bash","args":"{\"command\":\"ls\"}"}'
    sleep 0.3
    em '{"type":"tool_end","id":"t3","name":"bash","error":"exit 1: boom"}'
    sleep 0.4
    em '{"type":"text_delta","delta":"树完成。"}'
    em '{"type":"turn_end","index":0,"prompt_tokens":500,"completion_tokens":100,"cache_hit_tokens":400,"tool_calls":3}'
    em '{"type":"agent_end","turns":1,"session_id":"TREELIVE"}'
    ;;
  *)
    em '{"type":"agent_start","model":"deepseek-chat"}'
    em '{"type":"text_delta","delta":"桩应答："}'
    em "{\"type\":\"text_delta\",\"delta\":\"$MSG\"}"
    em '{"type":"agent_end","turns":1,"session_id":"TREED"}'
    ;;
esac
STUB
chmod +x "$TMP/bin/seek"
PROJ="$TMP/proj"

# ---- 3. 会话夹具：调用树（长结果/失败/orphan/XSS args）----
# 头行契约 = SaveTo 的 line 1；消息行 = deepseek.Message 原样 JSON。
# c_big 结果 5000 个 B + END_MARK：截断断言 = END_MARK 不进 DOM、
# 复制全文拿得到 END_MARK；tool 消息按 W4.4 契约全数独立成泡（零回归），
# 结果同时按 tool_call_id 登记进树行（行尾规模+展开面板）。
echo "== fixture =="
mkdir -p "$TMP/sessions"
FIX="$TMP/sessions/$TREE_SID.jsonl"
printf '{"schema_version":2,"id":"%s","created_at":"2026-11-20T10:00:00+08:00","updated_at":"2026-11-20T10:05:00+08:00","model":"deepseek-chat","cwd":"/tmp/tree-fixture","turns":2,"tool_calls":3,"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":100}}\n' "$TREE_SID" > "$FIX"
printf '{"role":"user","content":"树夹具：请读文件并跑一条命令。"}\n' >> "$FIX"
printf '{"role":"assistant","content":"","tool_calls":[{"id":"c_big","type":"function","function":{"name":"read","arguments":"{\\"path\\":\\"pkg/agent/agent.go\\",\\"offset\\":100}"}},{"id":"c_fail","type":"function","function":{"name":"bash","arguments":"{\\"command\\":\\"sleep 1\\"}"}}]}\n' >> "$FIX"
BIG=$(printf 'B%.0s' $(seq 1 5000))
printf '{"role":"tool","tool_call_id":"c_big","content":"%sEND_MARK"}\n' "$BIG" >> "$FIX"
printf '{"role":"tool","tool_call_id":"c_fail","content":"exit status 1: boom"}\n' >> "$FIX"
printf '{"role":"assistant","content":"读完了。"}\n' >> "$FIX"
printf '{"role":"tool","tool_call_id":"c_orphan","content":"orphan result（无对应调用——应独立成泡）"}\n' >> "$FIX"
printf '{"role":"assistant","content":"","tool_calls":[{"id":"c_xss","type":"function","function":{"name":"xss","arguments":"{\\"payload\\":\\"<script>window.__xssTree=1</script>\\"}"}}]}\n' >> "$FIX"
printf '{"role":"tool","tool_call_id":"c_xss","content":"xss result"}\n' >> "$FIX"
LINES=$(( $(wc -l < "$FIX" | tr -d ' ') - 1 ))
[ "$LINES" = 8 ] || { echo "FAIL: 夹具消息行 $LINES != 8"; exit 1; }

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
  if ! kill -0 "$SERVE_PID" 2>/dev/null; then
    echo "FAIL: serve 提前退出："; cat "$TMP/serve.log"; exit 1
  fi
  sleep 0.2
done
[ -n "$URL" ] || { echo "FAIL: 10s 内未见 banner"; exit 1; }
echo "$URL"
BASE="${URL%%\?*}"; BASE="${BASE%/}"   # 去尾斜杠：//msg 会被 mux 307 重定向
TOK="${URL#*token=}"

# ---- 5. curl 自检（服务端半边）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" 2>/dev/null | grep -qF "$2" && echo yes || echo no; }

page=$(curl -s "$BASE")
ck "GET / 200+页面" "yes" "$(printf '%s' "$page" | grep -q 'id="form"' && echo yes || echo no)"
ck "页面含流式工具树部件（toolTreeRow）" "yes" "$(has "$page" "toolTreeRow")"
ck "页面含耗时排版（fmtDur）" "yes" "$(has "$page" "fmtDur")"
ck "页面含会话树部件（ttree/tnode）" "yes" "$(has "$page" "ttree")"
ck "页面含复制全文按钮" "yes" "$(has "$page" "复制全文")"
ck "页面含渲染兜底占位" "yes" "$(has "$page" "（消息渲染失败）")"
ck "XSS 静态：无 innerHTML" "no" "$(has "$page" "innerHTML")"
ck "XSS 静态：无 insertAdjacentHTML" "no" "$(has "$page" "insertAdjacentHTML")"
ck "XSS 静态：无 document.write" "no" "$(has "$page" "document.write")"

# SSE：跑「树」场景，验 tool 事件族数量（6 帧：t1/t2 各 start+end、t3 start+end）
# 与 reply 收口带 run——c_ux7lvk 不改线，只改消费端。
: > "$TMP/sse.log"
curl -sN "$BASE/events?conv=curlconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d "{\"text\":\"/bind $PROJ\"}" > /dev/null
sleep 0.5
curl -s -X POST "$BASE/msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
  -H 'Content-Type: application/json' -d '{"text":"树"}' > /dev/null
sleep 5     # 桩全程 ~2s + 收口
kill "$SSE_PID" 2>/dev/null
wait "$SSE_PID" 2>/dev/null
SSE_PID=""

sse=$(cat "$TMP/sse.log")
ck "SSE：tool 事件族 6 帧（嵌套对+失败对）" "6" "$(printf '%s' "$sse" | grep -c '^event: tool$')"
ck "SSE：reply 收口带 run+session_id" "yes" "$(printf '%s' "$sse" | grep -q '"run":"' && printf '%s' "$sse" | grep -q '"session_id":"TREELIVE"' && echo yes || echo no)"
ck "SSE：§2.5 过滤 args/result 不下发" "no" "$(has "$sse" "2 matches")"

# /rpc：夹具会话可读、长结果全文在服务端（截断只在客户端）
rpc() { curl -s -X POST "$BASE/rpc?token=$TOK" -H 'Content-Type: application/json' -d "$1"; }
msgs=$(rpc "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/messages\",\"params\":{\"sid\":\"$TREE_SID\"}}")
ck "RPC：夹具 8 条单页（无 next_cursor）" "no" "$(has "$msgs" 'next_cursor')"
ck "RPC：tool 结果消息 4 条（c_big/c_fail/c_orphan/c_xss）" "4" "$(printf '%s' "$msgs" | grep -o '"tool_call_id"' | wc -l | tr -d ' ')"
ck "RPC：长结果全文在（END_MARK）" "yes" "$(has "$msgs" "END_MARK")"
ck "RPC：c_big 调用行在" "yes" "$(has "$msgs" '"c_big"')"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 6. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless（Chrome + node CDP）=="
  NODE_PID=""
  "$NODE_BIN" "$HERE/web-tree-headless.mjs" "$URL" "$PROJ" "$TREE_SID" --chrome "$CHROME_BIN" --dbg-port "$TREE_PORT" &
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
  1. /bind $PROJ → 发「树」：read 行缩进在 grep 行下、行尾耗时跳动→冻结；
     ✘ bash 行警示色；reply 到达整泡替换（工具行消失）
  2. 📚 会话 → 点 $TREE_SID：三个调用树行；点 read 行展开——参数全文 +
     结果摘要截断（尾部提示行），「复制全文」粘贴得含 END_MARK 整串；再点收起
  3. 孤儿 tool 消息独立成泡；xss 行 <script> 字面显示不执行
  4. 控制台 appendMsg({get role(){return 'assistant'}, get tool_calls(){throw 1}})
     → 「（消息渲染失败）」占位；再 appendMsg({role:'assistant',content:'ok'})
     正常成条——页面不白屏

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
