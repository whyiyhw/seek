#!/usr/bin/env bash
#
# web-bindpick-manual.sh — /bind、/resume 指令面的验收脚本（原 c_lnsdvu
# 「项目候选点选」：W5 的 6e5be518 退役了页面上的候选弹层，服务端契约与手打
# 指令面还在——这里守的就是它们）。
#
# 干什么：临时 SEEK_HOME 里造夹具（一个带 manifest + 会话的项目 proj-alpha、
# 一个只在 config serve.projects 里的 proj-beta），起 `seek serve
# --frontend=web`；先跑服务端自检（curl：/rpc project/list 形状、settings/get
# 的 projects 投影、/state 当前绑定、/projects 聊天文本回复逐字节不变、
# 不存在路径的「绑定失败：目录不存在」应答形状——页面不预检目录存在性，
# 靠的就是这条 Router 应答如实透传），再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-bindpick-headless.mjs 无头跑
#     （手打 /bind + /resume → /state 会话落定；手打不存在路径得失败应答且
#     绑定不动）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790（除非 ADDR 指向它，别这么干）。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 手打 /bind <proj-alpha> 回车，再 /resume <sid> → 聊天面
#      「已绑定项目…」「已续接会话…」
#   2. 手打 /bind <一个不存在的路径> → 聊天出「绑定失败：目录不存在：…」，
#      绑定不变
#
# Env:
#   ADDR=127.0.0.1:18799   主 serve 监听地址（写进临时 config.json）
#   CHROME=<路径>           无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手（butler 自测范式）
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误；
#         3 无头条件缺且 stdin 非 TTY（无人值守车道报「环境能力缺」）。
# 依赖：go、curl、shasum；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-bindpick-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18799}"    # 避开生产 8790 与 18790-18798（W2.1~W5 各卡）
SID="20261122-100000-bpfix0000000001"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 造夹具 \$TMP/projects/<id>/manifest.json（proj-alpha）+ \$TMP/sessions/$SID.jsonl（cwd=proj-alpha）"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\",\"projects\":[proj-alpha, proj-beta]}}"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（stderr 抓 banner URL）"
  echo "DRY_RUN: curl 自检：project/list 形状（root/sessions）；settings/get projects 投影；"
  echo "DRY_RUN:   /bind+/resume 后 /state 当前绑定；/projects 文本回复逐字节不变；不存在路径「绑定失败：目录不存在」"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言（手打 /bind + /resume、不存在路径）；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-bp-manual.XXXXXX)" || exit 1
SERVE_PID=""; SSE_PID=""
cleanup() {
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
  for p in "$SERVE_PID"; do
    [ -n "$p" ] && kill "$p" 2>/dev/null && wait "$p" 2>/dev/null
  done
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

# ---- 2. 夹具：proj-alpha（manifest + 一条会话，project/list 可见）、
#         proj-beta（只在 config 里——settings/get 候选的差集半边）----
echo "== fixtures =="
PROJ_A="$TMP/proj-alpha"
PROJ_B="$TMP/proj-beta"
mkdir -p "$PROJ_A" "$PROJ_B"
PID_A=$(printf '%s' "$PROJ_A" | shasum -a 256 | cut -c1-16)
mkdir -p "$TMP/projects/$PID_A"
printf '{"schema_version":1,"project_id":"%s","abs_path":"%s","first_seen":"2026-11-22T09:00:00Z","last_seen":"2026-11-22T09:30:00Z"}\n' \
  "$PID_A" "$PROJ_A" > "$TMP/projects/$PID_A/manifest.json"
mkdir -p "$TMP/sessions"
printf '{"schema_version":2,"id":"%s","created_at":"2026-11-22T10:00:00+08:00","updated_at":"2026-11-22T10:05:00+08:00","model":"deepseek-chat","cwd":"%s","turns":1,"tool_calls":0,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":10}}\n' \
  "$SID" "$PROJ_A" > "$TMP/sessions/$SID.jsonl"
printf '{"role":"user","content":"bindpick fixture：原始会话"}\n' >> "$TMP/sessions/$SID.jsonl"
printf '{"role":"assistant","content":"ok"}\n' >> "$TMP/sessions/$SID.jsonl"

# ---- 3. 临时 config + 起 serve，抓 banner URL ----
printf '{"serve":{"web_addr":"%s","projects":["%s","%s"]}}\n' "$ADDR" "$PROJ_A" "$PROJ_B" > "$TMP/config.json"
echo "== serve up =="
: > "$TMP/serve.log"
SEEK_HOME="$TMP" "$TMP/seek" serve --frontend=web 2> "$TMP/serve.log" &
SERVE_PID=$!
wait_url() { # wait_url <logfile> <名字>
  local url=""
  for _ in $(seq 1 50); do
    url=$(sed -n 's/^web: up: //p' "$1" | head -1)
    [ -n "$url" ] && { echo "$url"; return 0; }
    sleep 0.2
  done
  echo "FAIL: 10s 内未见 banner（$2）" >&2; exit 1
}
URL=$(wait_url "$TMP/serve.log" "主 serve")
echo "main:  $URL"
BASE="${URL%%\?*}";   TOK="${URL#*token=}"

# ---- 4. 服务端自检（/rpc 契约 + /state + /projects 字节钉）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF "$2" && echo yes || echo no; }
rpcc() { # rpcc <method> <params-json> → 响应体
  curl -s -X POST "${BASE}rpc?token=$TOK" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}
post() { # post <text> —— 以 curlconv 身份发一条消息（路径来自 mktemp，无转义面）
  curl -s -X POST "${BASE}msg?token=$TOK" -H 'X-Seek-Conversation: curlconv' \
    -H 'Content-Type: application/json' -d "{\"text\":\"$1\"}" > /dev/null
}

# project/list：只有 proj-alpha（proj-beta 无 manifest）；sessions=1。
r=$(rpcc project/list '{}')
ck "project/list 恰一个项目" "1" "$(printf '%s' "$r" | grep -o '"root":"' | wc -l | tr -d '[:space:]')"
ck "project/list root = proj-alpha" "yes" "$(has "$r" "$PROJ_A")"
ck "project/list proj-alpha sessions=1" "yes" "$(printf '%s' "$r" | grep -q '"sessions":1' && echo yes || echo no)"
ck "project/list 无 proj-beta" "no" "$(has "$r" "$PROJ_B")"

# settings/get：config serve.projects 原序投影（/projects 候选的第二个来源）。
r=$(rpcc settings/get '{}')
ck "settings/get projects 投影两项" "yes" "$(printf '%s' "$r" | grep -q "\"projects\":\[\"$PROJ_A\",\"$PROJ_B\"\]" && echo yes || echo no)"

# /state：/bind + /resume 落定「已绑定 proj-alpha · 会话 SID」——浏览器无头
# 再验页面侧同一条链。
post "/bind $PROJ_A"; sleep 0.4
post "/resume $SID";  sleep 0.4
st=$(curl -s "${BASE}state?token=$TOK" -H 'X-Seek-Conversation: curlconv')
ck "/state bound=true" "yes" "$(printf '%s' "$st" | grep -q '"bound":true' && echo yes || echo no)"
ck "/state project = proj-alpha" "yes" "$(has "$st" "$PROJ_A")"
ck "/state session = $SID" "yes" "$(printf '%s' "$st" | grep -q "\"session\":\"$SID\"" && echo yes || echo no)"

# /projects：聊天文本回复逐字节不变（cmdProjects 零改动）。SSE 抓默认帧，
# 对 Go json.Marshal 的 \n 转义拼写做固定串比对——两个空格的 ← 当前 标记、
# 全角括号的会话注记都在内。
: > "$TMP/sse.log"
curl -sN "${BASE}events?conv=curlconv&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5   # 等订阅落册（MVP 无回放：先订后发）
post "/projects"; sleep 0.6
post "/bind $TMP/gone-project"; sleep 0.4   # 不存在路径：页面不预检，靠这条应答
kill "$SSE_PID" 2>/dev/null; wait "$SSE_PID" 2>/dev/null; SSE_PID=""
sse=$(cat "$TMP/sse.log")
EXPECT="\"text\":\"候选项目：\\n• $PROJ_A  ← 当前\\n• $PROJ_B\\n当前绑定：${PROJ_A}（会话 ${SID}）\""
ck "/projects 文本回复逐字节不变" "yes" "$(has "$sse" "$EXPECT")"
ck "不存在路径 → 绑定失败：目录不存在" "yes" "$(has "$sse" "绑定失败：目录不存在：$TMP/gone-project")"

[ "$fail" = 0 ] || { echo "自检有 FAIL——手测前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = "1" ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-bindpick-headless.mjs" "$URL" "$PROJ_A" "$SID" \
    --gone "$TMP/gone-project" --chrome "$CHROME_BIN" || exit 1
  echo "无头验收全绿。现场已清理（KEEP=1 可保留复跑）"
  exit 0
fi

# 无头条件缺 + 非 TTY = 无人值守车道：人工清单无处可走（read 要么挂到
# 超时、要么 stdin 已关时读空即过，给出无凭据的绿）。退 3 点名缺哪样——
# 与 web-rename/web-sessions manual 同一口径（docs/pitfalls/tooling
# 「手测脚本的『等人按 Enter』在无人值守车道会变成假绿」）。
if [ ! -t 0 ]; then
  miss=""
  [ -x "$CHROME_BIN" ] || miss="Chrome（${CHROME_BIN}）"
  [ -n "$NODE_BIN" ] || miss="${miss:+${miss} + }node"
  echo "FAIL: 无头验收条件缺（${miss}）且 stdin 非 TTY——无人值守没有人工清单可走。装上缺的组件或 CHROME=<路径> 重跑。" >&2
  exit 3
fi

echo
echo "== 人工清单（无 Chrome 或 node≥22 时）=="
echo "1. 手打 /bind $PROJ_A 回车，再 /resume $SID → 聊天面「已绑定项目…」「已续接会话…」"
echo "2. 手打 /bind $TMP/gone-project → 「绑定失败：目录不存在：…」，绑定不变"
echo "按 Enter 收尾（清理现场）…"
read _
exit 0
