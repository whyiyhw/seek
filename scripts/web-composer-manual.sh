#!/usr/bin/env bash
#
# web-composer-manual.sh — W6-4 composer 卡片 + 主区几何的无头量测入口
# （卡 c_ljtmy4 验收②；c_mo7quv 返工时把一次性探针落成脚本——探针不入库，
# 车道拒跑＝「验证未跑成」）。
#
# 干什么：临时 SEEK_HOME + 一条夹具会话 + 夹具项目目录，起
# `seek serve --frontend=web`，由 scripts/web-composer-headless.mjs 经 CDP 量：
#   ① 空态：composer 卡片中心 y ∈ [380,520]、宽 774±2、左右边距差 ≤2px；
#   ② 有消息：卡片底距视口底 ≤40px；
#   ③ 会话头只在主栏里（左缘 ≥ 侧栏右缘），空态/看历史/面板三种状态都量；
#   ④ 面板打开时聊天面整体让位（#list / #hist / form / #composer-meta 隐藏）。
# 全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（Chrome/node 缺失时脚本原样打印）：打开脚本打印的 URL，
# 1440×900 下用 DevTools 量上面四条。
#
# Env:
#   CHROME=<路径>   无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1       只打印步骤不动手
#   KEEP=1          结束后保留临时目录（serve 已停；自行清理）
#
# 退出码：0 量测全过；1 失败；2 用法错误；3 无头条件缺且 stdin 非 TTY
#         （无人值守车道：报环境能力缺，不假装绿）。
# 依赖：go、curl；无头量测另需 Chrome 与 node≥22。
set -u

usage() {
  echo "用法：bash scripts/web-composer-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "$HERE")"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: 选随机空闲高端口（28000–42000，避开 8790/187xx/188xx）"
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 夹具会话 \$TMP/sessions/<sid>.jsonl（2 条消息）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner URL）"
  echo "DRY_RUN: node scripts/web-composer-headless.mjs <URL> <proj> <sid> → 四组量测"
  exit 0
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/seek-w6composer.XXXXXX")" || exit 1
SID="20261115-100000-w64composergeom01"
PROJ="$TMP/proj"
ADDR=""
SERVE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then
    echo "KEEP=1：保留沙盒 ${TMP}（serve 已停；自行清理）"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

# ---- 1. 随机空闲高端口（避开生产 8790 与既有手测脚本的 187xx/188xx 段）----
free_port() {
  local p i
  for i in $(seq 1 40); do
    p=$(( (RANDOM % 14000) + 28000 ))
    case "$p" in 8790|1879[0-9]|1885[0-9]) continue ;; esac
    if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
      exec 3>&- 3<&-
    else
      printf '%s' "$p"; return 0
    fi
  done
  return 1
}
P="$(free_port)" || { echo "FAIL: 40 次掷端口无空闲"; exit 1; }
ADDR="127.0.0.1:$P"

# ---- 2. 构建 ----
echo "== build =="
(cd "$REPO" && go build -o "$TMP/seek" ./cmd/seek) || exit 1

# ---- 3. 夹具：一条会话 + 它自记的项目目录 ----
# 项目目录必须真实存在：执行器以它为子进程的工作目录（cmd.Dir）。
echo "== fixtures =="
mkdir -p "$TMP/sessions" "$PROJ"
printf '{"schema_version":3,"id":"%s","created_at":"2026-11-15T10:00:00+08:00","updated_at":"2026-11-15T10:05:00+08:00","model":"deepseek-chat","cwd":"%s","turns":1,"tool_calls":0,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}\n' "$SID" "$PROJ" > "$TMP/sessions/$SID.jsonl"
printf '{"role":"user","content":"几何夹具：你好"}\n' >> "$TMP/sessions/$SID.jsonl"
printf '{"role":"assistant","content":"几何夹具：收到。"}\n' >> "$TMP/sessions/$SID.jsonl"
printf '{"serve":{"web_addr":"%s"}}\n' "$ADDR" > "$TMP/config.json"

# ---- 4. 起 serve，抓 banner URL ----
echo "== serve up（${ADDR}）=="
: > "$TMP/serve.log"
SEEK_HOME="$TMP" "$TMP/seek" serve --frontend=web 2> "$TMP/serve.log" &
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
case "$URL" in
  *:8790/*) echo "FAIL: banner 落在生产端口 8790：${URL}"; exit 1 ;;
esac
echo "$URL"

# ---- 5. 无头量测，或人工清单 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = "1" ]; then
  echo "== headless 量测 =="
  DP=$(( (RANDOM % 100) + 28000 ))
  "$NODE_BIN" "$HERE/web-composer-headless.mjs" "$URL" "$PROJ" "$SID" \
    --chrome "$CHROME_BIN" --dbg-port "$DP"
  rc=$?
  [ "$rc" = 0 ] || exit 1
  echo "无头量测全绿。现场已清理（KEEP=1 可保留复跑）"
  exit 0
fi

# 无头条件缺 + 非 TTY = 无人值守车道：人工清单无处走（c_kwiajf 判据）。
if [ ! -t 0 ]; then
  miss=""
  [ -x "$CHROME_BIN" ] || miss="Chrome（${CHROME_BIN}）"
  [ -n "$NODE_BIN" ] || miss="${miss:+${miss} + }node"
  echo "FAIL: 无头量测条件缺（${miss}）且 stdin 非 TTY——无人值守没有人工清单可走。装上缺的组件或 CHROME=<路径> 重跑。" >&2
  exit 3
fi
cat <<EOF

== 无头条件缺（Chrome/node）——人工量测清单 ==
  打开：$URL
  1440×900，DevTools 里量：
  1. 空态：#center > form 的中心 y ∈ [380,520]、宽 774±2、左右边距差 ≤2px
  2. 发一条消息后：form 底距视口底 ≤40px
  3. #conv-head 左缘 ≥ 侧栏右缘（空态 / 看历史 / 面板打开 三种状态）
  4. 面板打开时 #list、#hist、form、#composer-meta 都是 display:none

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
