#!/usr/bin/env bash
#
# web-dirpick-manual.sh — 卡 c_heffj3（fs/list 只读目录 method + 页面
# 目录浏览器）的验收脚本。
#
# 干什么：临时 SEEK_HOME + 假 HOME（坑 8：全程不碰真实 ~/.seek 与生产
# 8790）里造目录夹具，配置 serve.project_roots / serve.projects，起
# `seek serve --frontend=web`，先跑服务端自检（curl /rpc 的 fs/list
# 契约：缺省回首根 / 下钻 / 根外 -32602 逃生口文案 / limit 截断；
# 外加旧 /projects 文本回复不回归），再走浏览器验收：
#   - 本机有 Chrome + node≥22 → scripts/web-dirpick-headless.mjs 无头
#     跑全部断言（开候选 → 📁 浏览目录 → 过滤 → 进两级 → 上一级 →
#     选当前目录后 /state 的 project 变该路径且 session 清空 →
#     目录消失的层内错误提示）；
#   - 缺任一 → 打印人工清单等人按 Enter。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 输入框打「/bind 」→ 弹层出「📁 浏览目录…」
#   2. 点它 → 层头显示根路径；过滤框打字 → 行随过滤收窄
#   3. 点子目录两级 → 点「↑ 上一级」回到父层
#   4. 点「选这个目录」→ 状态行变已绑定该路径；/state 的 session 为空
#   5. 层内点一个已被外部删除的目录行 → 弹层内一行错误提示，不白屏
#
# Env:
#   ADDR=127.0.0.1:18801  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1             只打印步骤不动手（butler 自测范式）
#   KEEP=1                结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误；
#         3 无头条件缺且 stdin 非 TTY（无人值守车道，c_gqo32q 口径）。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-dirpick-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18801}"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 夹具 \$TMP/dirpick/{proja/{sub1,sub2,vanish},probj} + 文件与隐藏目录"
  echo "DRY_RUN: 写 \$TMP/config.json {serve:{web_addr,projects,project_roots}}"
  echo "DRY_RUN: SEEK_HOME=\$TMP HOME=\$TMP \$TMP/seek serve --frontend=web &（stderr 抓 banner URL）"
  echo "DRY_RUN: curl 自检：fs/list 缺省回首根/下钻/根外 -32602 文案/limit 截断/show_hidden；/projects 文本不回归"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-dirpick-manual.XXXXXX)" || exit 1
SERVE_PID=""
SSE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SSE_PID" ] && kill "$SSE_PID" 2>/dev/null
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

# ---- 2. 夹具：目录树（fs/list 的可浏览面）----
echo "== fixtures =="
mkdir -p "$TMP/dirpick/proja/sub1" "$TMP/dirpick/proja/sub2" "$TMP/dirpick/proja/vanish" "$TMP/dirpick/probj"
echo x > "$TMP/dirpick/afile.txt"
mkdir -p "$TMP/dirpick/.hiddendir"
echo x > "$TMP/dirpick/.hiddenfile"
# TMP 在 macOS 是 /var → /private/var 的符号链；fs/list 回的是解析后路径，
# 断言一律用物理路径。
ROOT="$(cd "$TMP" && pwd -P)"

# ---- 3. 临时 config + 起 serve，抓 banner URL ----
# HOME 也指进沙盒：fs/list 的隐式 HOME 根随假 HOME 走，不读真家目录。
printf '{"serve":{"web_addr":"%s","projects":["%s/dirpick/proja"],"project_roots":["%s/dirpick"]}}\n' \
  "$ADDR" "$ROOT" "$ROOT" > "$TMP/config.json"
echo "== serve up =="
: > "$TMP/serve.log"
SEEK_HOME="$TMP" HOME="$TMP" "$TMP/seek" serve --frontend=web 2> "$TMP/serve.log" &
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
BASE="${URL%%\?*}"
TOK="${URL#*token=}"

# ---- 4. 服务端自检（/rpc 契约；UI 消费的同一条路）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
rpcc() { # rpcc <method> <params-json> → 响应体
  curl -s -X POST "${BASE}rpc?token=$TOK" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}
getcode() { printf '%s' "$1" | grep -o '"code":-[0-9]*' | head -1 | cut -d: -f2; }

# 缺省 path = 首个可浏览根（project_roots[0]）——页面自描述起点
r=$(rpcc fs/list '{}')
ck "缺省 path 回首根" "yes" "$(printf '%s' "$r" | grep -q "\"path\":\"$ROOT/dirpick\"" && echo yes || echo no)"
ck "缺省 parent 为空（根上不可再上）" "no" "$(printf '%s' "$r" | grep -q '"parent"' && echo yes || echo no)"
ck "下钻列出 proja/projb（is_dir=true）" "yes" \
  "$(printf '%s' "$r" | grep -q '"name":"proja","path":"'"$ROOT"'/dirpick/proja","is_dir":true' \
   && printf '%s' "$r" | grep -q '"name":"probj"' && echo yes || echo no)"
ck "文件行带 is_dir=false（过滤是页面的事）" "yes" \
  "$(printf '%s' "$r" | grep -q '"name":"afile.txt"[^}]*"is_dir":false' && echo yes || echo no)"
ck "缺省滤掉点条目" "no" "$(printf '%s' "$r" | grep -q 'hidden' && echo yes || echo no)"
r=$(rpcc fs/list "{\"path\":\"$ROOT/dirpick\",\"show_hidden\":true}")
ck "show_hidden 带回点条目" "yes" "$(printf '%s' "$r" | grep -q '"name":".hiddendir"' && echo yes || echo no)"

# 下钻两级 + parent 链
r=$(rpcc fs/list "{\"path\":\"$ROOT/dirpick/proja/sub1\"}")
ck "下钻 sub1 path 正确" "yes" "$(printf '%s' "$r" | grep -q "\"path\":\"$ROOT/dirpick/proja/sub1\"" && echo yes || echo no)"
ck "sub1 parent 指 proja" "yes" "$(printf '%s' "$r" | grep -q '"parent":"'"$ROOT"'/dirpick/proja"' && echo yes || echo no)"

# 根外：TMP 的父目录（/tmp 或 /private/tmp）不在根集
r=$(rpcc fs/list "{\"path\":\"$(dirname "$ROOT")\"}")
ck "根外 -32602" "-32602" "$(getcode "$r")"
ck "根外文案带逃生口" "yes" \
  "$(printf '%s' "$r" | grep -q '不在可浏览根内：编辑 ~/.seek/config.json 的 serve.project_roots' && echo yes || echo no)"
ck "根外不返回 entries（不落到 ReadDir）" "no" "$(printf '%s' "$r" | grep -q '"entries"' && echo yes || echo no)"
r=$(rpcc fs/list '{"path":"/etc"}')
ck "根外（/etc）-32602" "-32602" "$(getcode "$r")"

# 路径不存在 / 是文件（根内）
r=$(rpcc fs/list "{\"path\":\"$ROOT/dirpick/nope\"}")
ck "根内不存在 -32602" "-32602" "$(getcode "$r")"
ck "不存在文案" "yes" "$(printf '%s' "$r" | grep -q '不是目录或目录不存在' && echo yes || echo no)"
r=$(rpcc fs/list "{\"path\":\"$ROOT/dirpick/afile.txt\"}")
ck "是文件 -32602" "-32602" "$(getcode "$r")"

# limit 截断
r=$(rpcc fs/list "{\"path\":\"$ROOT/dirpick\",\"limit\":2}")
ck "limit 截断 + truncated" "yes" \
  "$(printf '%s' "$r" | grep -q '"truncated":true' && [ "$(printf '%s' "$r" | grep -o '"name":' | wc -l | tr -d ' ')" = "2" ] && echo yes || echo no)"

# 旧 /projects 文本回复不回归：SSE 抓默认帧里的候选项目 bullet（历史格式逐字节）
curl -sN "${BASE}events?conv=dirpickcurl&token=$TOK" > "$TMP/sse.log" &
SSE_PID=$!
sleep 0.5
curl -s -X POST "${BASE}msg?token=$TOK" -H 'Content-Type: application/json' \
  -H 'X-Seek-Conversation: dirpickcurl' -d '{"text":"/projects"}' > /dev/null
sleep 1
ck "/projects 回复含候选头" "yes" "$(grep -q '候选项目' "$TMP/sse.log" && echo yes || echo no)"
ck "/projects 回复含配置项目 bullet（历史格式）" "yes" \
  "$(grep -q "• $ROOT/dirpick/proja" "$TMP/sse.log" && echo yes || echo no)"
kill "$SSE_PID" 2>/dev/null; wait "$SSE_PID" 2>/dev/null; SSE_PID=""

[ "$fail" = 0 ] || { echo "自检有 FAIL——手测前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = "1" ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-dirpick-headless.mjs" "$URL" "$ROOT" \
    --chrome "$CHROME_BIN" &
  NODE_PID=$!
  wait "$NODE_PID"; rc=$?
  NODE_PID=""
  [ "$rc" = 0 ] || exit 1
  echo "无头验收全绿。现场已清理（KEEP=1 可保留复跑）"
  exit 0
fi

# 无头条件缺 + 非 TTY = 无人值守车道：人工清单无处可走（read 要么挂到
# 超时、要么 stdin 已关时读空即过，给出无凭据的绿）。退 3 点名缺哪样——
# 与 web-sessions/web-rename 同一口径（c_kwiajf / c_gqo32q）。
if [ ! -t 0 ]; then
  miss=""
  [ -x "$CHROME_BIN" ] || miss="Chrome（${CHROME_BIN}）"
  [ -n "$NODE_BIN" ] || miss="${miss:+${miss} + }node"
  echo "FAIL: 无头验收条件缺（${miss}）且 stdin 非 TTY——无人值守没有人工清单可走。装上缺的组件或 CHROME=<路径> 重跑。" >&2
  exit 3
fi

echo
echo "== 人工清单（无 Chrome 或 node≥22 时）=="
echo "1. 打开 $URL"
echo "2. 输入框打「/bind 」→ 弹层出「📁 浏览目录…」；点它进目录模式"
echo "3. 层头显示 \$ROOT/dirpick；过滤框打 proja → 只剩一行；清空过滤"
echo "4. 点 proja → sub1（进两级）→「↑ 上一级」回 proja"
echo "5. 点「选这个目录」→ 状态行已绑定该路径（会话重置）"
echo "6. 重开浏览器目录层，点 vanish（脚本已在层开后删除它）→ 层内一行错误，不白屏"
echo "   （vanish 由本脚本在无头分支删除；人工验证可用任意先列后删的目录）"
echo "按 Enter 收尾（清理现场）…"
read _
exit 0
