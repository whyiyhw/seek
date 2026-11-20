#!/usr/bin/env bash
#
# web-input-manual.sh — 输入增强（卡 c_7lsngm）的手测/无头验收脚本。
#
# 卡面验收（沙盒）：①三组交互键盘路径全通（Enter 发送 / Shift+Enter 换行；
# '/' 指令补全；'@' 会话引用）；②弹层 Esc 关闭且不劫持全局键；③窄屏可降级；
# ④web-gate 过、go test ./... 绿。
#
# 干什么：临时 SEEK_HOME + 桩 seek（PATH 前置劫持——serve 无 SeekPath 出口，
# executor 按名字找 seek）+ 桩项目目录 + 一条会话夹具（'@' 的候源），起
# `seek serve --frontend=web`；先跑服务端自检（页面部件 / 无全局 keydown
# 监听 / /rpc session/list 里夹具可见），再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-input-headless.mjs 无头跑全部
#     断言（真按键走 CDP Input 域：三组键盘路径 + Esc 不劫持 + 窄屏降级）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 输入「第一行」→ Shift+Enter →「第二行」：框里出现两行；单独 Enter
#      发出这条两行消息，输入框清空。
#   2. 输入 /：上方弹命令面板（/bind /projects /switch /new /cancel /resume）；
#      ↑↓ 移高亮；Tab 或 Enter 补全成「/bind 」（带参指令留空格）；
#      Esc 关闭且草稿保留；面板关着时 Esc 页面上没有别的东西被吃掉。
#   3. 输入 @：弹出最近会话（来自 session/list）；↑↓ 选一条按 Enter ——
#      草稿里出现 @<sid>，同时发出 /resume <sid>，顶栏状态行会话变成该 sid。
#   4. 把窗口拉到 390px 宽：面板满宽、条目换行、输入仍可打字，Tab 仍能补全。
#
# Env:
#   ADDR=127.0.0.1:18796  监听地址（写进临时 config.json 的 serve.web_addr；
#                          避开生产 8790 与既有 web-* 脚本的 18790/91/95）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-input-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18796}"
SID="20261119-120000-wtinfixture01"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/bin/seek 桩（/bind 走路由指令，普通消息回一条固定应答）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 造会话夹具 \$TMP/sessions/$SID.jsonl（cwd=\$TMP/proj，'@' 的候源）"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: PATH=\$TMP/bin:\$PATH SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：页面含 textarea#input / #menu / CMDS 注册表 / 无全局 keydown；session/list 含夹具"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言（三组键盘路径+Esc 不劫持+窄屏）；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-wtin-manual.XXXXXX)" || exit 1
SERVE_PID=""
NODE_PID=""
cleanup() {
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

# ---- 2. 桩 seek + 桩项目 + 会话夹具 ----
echo "== stub =="
mkdir -p "$TMP/bin" "$TMP/proj" "$TMP/sessions"
cat > "$TMP/bin/seek" <<'STUB'
#!/usr/bin/env bash
# 输入增强手测桩：普通消息回一条固定应答；/bind 之类指令由平面自己答，
# 永不 exec（路由层拦下）。所以这里只需一个良性的 happy 应答。
set -u
MSG=""
RESUME=""
while [ $# -gt 0 ]; do
  case "$1" in
    -p) MSG="${2:-}"; shift 2 ;;
    -resume) RESUME="${2:-}"; shift 2 ;;
    *) shift ;;
  esac
done
em() { printf '%s\n' "$1"; }
em '{"type":"agent_start","model":"deepseek-chat"}'
# -resume 时回同一 id（真 seek 的行为），否则铸一个新的。
SID="${RESUME:-SWTIN001}"
em "{\"type\":\"text_delta\",\"delta\":\"桩应答：$MSG\"}"
em "{\"type\":\"agent_end\",\"turns\":1,\"session_id\":\"$SID\"}"
STUB
chmod +x "$TMP/bin/seek"
PROJ="$TMP/proj"

# 会话夹具：头行 = SaveTo 的 line 1 形状（Session 去掉 messages），
# cwd 指向桩项目——'@' 选中后 /resume 的「项目随会话」断言就靠这个值。
printf '{"schema_version":3,"id":"%s","created_at":"2026-11-19T12:00:00Z","updated_at":"2026-11-19T12:30:00Z","model":"deepseek-chat","cwd":"%s","turns":2,"tool_calls":0,"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}\n' \
  "$SID" "$PROJ" > "$TMP/sessions/$SID.jsonl"
printf '{"role":"user","content":"夹具会话：输入增强验收用"}\n' >> "$TMP/sessions/$SID.jsonl"
printf '{"role":"assistant","content":"收到。"}\n' >> "$TMP/sessions/$SID.jsonl"

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
ck() {
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
# grep -q 早退会让 printf 吃 SIGPIPE（bash 3.2 下刷一屏噪音）；
# 用 -c 读完全部输入，只借退出码判真值。
has() { [ "$(printf '%s' "$1" | grep -cF "$2")" -gt 0 ] && echo yes || echo no; }

page=$(curl -s "$BASE")
ck "GET / 200+页面" "yes" "$(has "$page" 'id="form"')"
ck "多行输入：textarea#input" "yes" "$(has "$page" '<textarea id="input"')"
ck "候选弹层：#menu" "yes" "$(has "$page" 'id="menu"')"
ck "'/' 指令注册表" "yes" "$(has "$page" 'const CMDS = [')"
ck "注册表含五指令 + /resume" "yes" \
  "$([ "$(printf '%s' "$page" | grep -c "name: '/bind'")" -gt 0 ] \
     && [ "$(printf '%s' "$page" | grep -c "name: '/cancel'")" -gt 0 ] \
     && [ "$(printf '%s' "$page" | grep -c "name: '/resume'")" -gt 0 ] && echo yes || echo no)"
ck "'@' 候源 = session/list" "yes" "$(has "$page" "rpc('session/list', {})")"
ck "无全局 keydown 监听（Esc 不劫持）" "0" \
  "$(printf '%s' "$page" | grep -cE "document\.addEventListener\('keydown'|window\.addEventListener\('keydown'")"
ck "窄屏降级有媒体查询" "yes" "$(has "$page" 'max-width: 640px')"
ck "页面无 HTML 解析 sink（3 项）" "0" \
  "$(printf '%s' "$page" | grep -cE 'innerHTML|insertAdjacentHTML|document\.write')"

list=$(curl -s -X POST "$BASE/rpc?token=$TOK" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}')
ck "'@' 的候源里能看到夹具会话" "yes" "$(has "$list" "$SID")"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless（Chrome + node CDP 真按键）=="
  "$NODE_BIN" "$HERE/web-input-headless.mjs" "$URL" "$PROJ" "$SID" --chrome "$CHROME_BIN" &
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
  1. 输入「第一行」→ Shift+Enter →「第二行」：框里两行；单独 Enter 发出
     这条两行消息、输入框清空
  2. 输入 / ：上方弹面板（/bind /projects /switch /new /cancel /resume）；
     ↑↓ 移高亮、Tab 或 Enter 补全成「/bind 」（带参指令留空格）、Esc 关闭
     且草稿保留；面板关着时再按 Esc，页面上没有别的东西被吃掉
  3. 输入 @ ：弹出最近会话（夹具 $SID）；↑↓ 选它按 Enter —— 草稿出现
     @$SID，同时发出 /resume $SID，顶栏状态行会话变成该 sid
  4. 窗口拉到约 390px 宽：面板满宽、条目换行、输入 16px，Tab 仍能补全

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
