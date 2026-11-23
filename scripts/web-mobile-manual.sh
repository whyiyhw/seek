#!/usr/bin/env bash
#
# web-mobile-manual.sh — 390px 手机宽响应式的沙盒验收脚本（卡 c_lqtt2m）。
# 卡面验收①「390px 全页面无不可达入口/不可读裁切」的仓内可复现半边：
# 上一轮的断言只存在于仓外沙盒（~/.butler-jobs/…），返工轮把它落进仓。
#
# 干什么（仿 scripts/web-theme-manual.sh）：临时 SEEK_HOME + 非默认 config +
# 两条会话夹具（长中文标题 / 长路径 / 围栏代码长行都盖到）→ 起
# `seek serve --frontend=web` → 无头浏览器（scripts/web-mobile-headless.mjs）
# 跑断言并出两主题八面截图。全程不碰真实 ~/.seek 与生产 8790。
#
# 断言面（详见 headless 脚本头注释）：缺省收起 44px 轨 + 展开态抽屉化、
# 收起轨图标钮全部在轨内且 hit-test 命中自己（返工轮补的 ⟩ 被「💬 聊天」
# / 设置标题盖住那条）、按钮组换行全显、页面级零横向溢出、设置行纵堆叠 +
# ellipsis、资产面板纵堆叠、代码块自持横滚。
#
# Env:
#   ADDR=127.0.0.1:18811   监听地址（写进临时 config.json 的 serve.web_addr）
#   CDP_PORT=18813         无头 Chrome 的调试端口
#   SHOT_DIR=<目录>        截图落点（默认在临时目录内，脚本结束时随现场清掉；
#                          想看就把 KEEP=1 或指定一个仓外目录）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1              只打印步骤不动手
#   KEEP=1                 结束后保留临时目录与截图（serve 已停）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误；
#         3 无头条件缺且 stdin 非 TTY（无人值守车道：报环境能力缺，不假装绿）。
# 依赖：go；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-mobile-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18811}"   # 避开生产 8790 与既有脚本的 18790–18798

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 夹具：长路径候选项目 + 两条会话（长中文标题 / 围栏代码长行 / 归档位）"
  echo "DRY_RUN: 写 \$TMP/config.json（serve: web_addr=$ADDR, max_concurrency=6, yolo=true,"
  echo "DRY_RUN:   projects=[桩项目], default_provider=deepseek, auto_compact.at_tokens=350000）"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: 浏览器：390×844 @2x 设备模拟 → 两主题八面截图 + 断言"
  echo "DRY_RUN:   （收起轨可达/抽屉化/按钮组换行/零横向溢出/设置堆叠+ellipsis/pre 横滚）"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5mobile-manual.XXXXXX)" || exit 1
SHOT_DIR="${SHOT_DIR:-$TMP/shots}"
SERVE_PID=""
cleanup() {
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then
    # ${TMP} 花括号不可省：macOS bash 3.2 在 set -u 下把紧跟多字节字符的
    # $TMP 误读进变量名（$TMP（ → "TMP<EFBBBF>: unbound variable"）。
    echo "KEEP=1：保留 ${TMP}（serve 已停；截图在 ${SHOT_DIR}；自行清理）"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

# ---- 1. 构建 ----
echo "== build =="
(cd "$REPO" && go build -o "$TMP/seek" ./cmd/seek) || exit 1

# ---- 2. 夹具：长路径候选项目 + 两条会话 ----
echo "== fixtures =="
# 长路径候选项目（设置面板 ellipsis 断言用——值里带 /）
PROJ="$TMP/projects/a-rather-long-project-name-to-exercise/the-settings-ellipsis-row/proj"
mkdir -p "$PROJ" "$TMP/sessions"
# 会话 1：长中文标题 + 代码块长行（会话正文渲染 markdown → pre 横滚协同）
SID1="20261122-090000-mobilefixture01"
printf '{"schema_version":3,"id":"%s","created_at":"2026-11-22T09:00:00+08:00","updated_at":"2026-11-22T09:05:00+08:00","model":"deepseek-chat","cwd":"%s","turns":2,"tool_calls":0,"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_cache_hit_tokens":600,"prompt_cache_miss_tokens":400}}\n' \
  "$SID1" "$PROJ" > "$TMP/sessions/$SID1.jsonl"
printf '{"role":"user","content":"帮我看一下这个手机宽度下气泡与代码块的排版是否可读，长中文段落要能自然换行而不是每行只有四五个字。"}\n' >> "$TMP/sessions/$SID1.jsonl"
printf '{"role":"assistant","content":"代码块长行验证：\\n```go\\nfunc main() { println(\\"ok-with-a-rather-long-line-to-exercise-horizontal-scrolling-inside-the-pre-block\\") }\\n```\\n以上。"}\n' >> "$TMP/sessions/$SID1.jsonl"
# 会话 2：归档位 + 中标题（列表两行，抽屉截图不空）
SID2="20261122-080000-mobilefixture02"
printf '{"schema_version":3,"id":"%s","created_at":"2026-11-22T08:00:00+08:00","updated_at":"2026-11-22T08:10:00+08:00","model":"deepseek-reasoner","cwd":"%s","turns":1,"tool_calls":0,"archived":true,"usage":{"prompt_tokens":500,"completion_tokens":80,"total_tokens":580,"prompt_cache_hit_tokens":300,"prompt_cache_miss_tokens":280}}\n' \
  "$SID2" "$PROJ" > "$TMP/sessions/$SID2.jsonl"
printf '{"role":"user","content":"第二条会话（归档）"}\n' >> "$TMP/sessions/$SID2.jsonl"

# ---- 3. 临时 config ----
cat > "$TMP/config.json" <<EOF
{
  "default_provider": "deepseek",
  "auto_compact": {"at_tokens": 350000},
  "serve": {
    "web_addr": "$ADDR",
    "max_concurrency": 6,
    "yolo": true,
    "projects": ["$PROJ"]
  }
}
EOF

# ---- 4. 起 serve，抓 banner URL ----
echo "== serve up =="
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
echo "$URL"

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless（390px @2x，Chrome + node CDP）=="
  mkdir -p "$SHOT_DIR"
  "$NODE_BIN" "$HERE/web-mobile-headless.mjs" "$URL" "$SHOT_DIR" \
    --chrome "$CHROME_BIN" --cdp-port "${CDP_PORT:-18813}"
  rc=$?
  [ "$rc" = 0 ] || exit 1
  echo "无头验收全绿（截图见 ${SHOT_DIR}）"
  exit 0
fi

# 无头条件缺 + 非 TTY = 无人值守车道：人工清单无处可走。read 在这里要么
# 挂到车道超时，要么 stdin 已关时读空即过——后者会给出无凭据的绿。退 3，
# 点名缺哪样能力（c_kwiajf 判据，web-sessions-manual.sh:221-230 同款）。
if [ ! -t 0 ]; then
  miss=""
  [ -x "$CHROME_BIN" ] || miss="Chrome（${CHROME_BIN}）"
  [ -n "$NODE_BIN" ] || miss="${miss:+${miss} + }node"
  echo "FAIL: 无头验收条件缺（${miss}）且 stdin 非 TTY——无人值守没有人工清单可走。装上缺的组件或 CHROME=<路径> 重跑。" >&2
  exit 3
fi

cat <<EOF

== 无头条件缺（Chrome/node）——人工手测清单（浏览器 390px 宽）==
  打开：$URL
  1. 开发者工具切成 390×844：侧栏是 44px 图标轨（＋/🗄/⟩ 三个钮都完整、
     都点得到）；点 ⟨/⟩ 能开合；展开态盖住中栏、点遮罩收
  2. 顶栏：/cancel 与 🪝 审计 全文可见，按钮组换行不裁
  3. 点侧栏会话：正文单栏满宽，长代码行在代码块内横滚不越气泡
  4. ⚙ 设置：每行 label 在上值在下，长路径/hash 以 … 结尾不碎行
  5. 亮暗两主题各过一遍；页面无横向滚动条

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _
echo "done"
