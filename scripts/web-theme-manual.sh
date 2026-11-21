#!/usr/bin/env bash
#
# web-theme-manual.sh — W5 卡 c_ni7mzw（主题切换 + 设置面板）的沙盒验收脚本
# （feature-web-frontend §13.1 c_ni7mzw 行）。卡面验收四项全在沙盒内自证：
#   ① 切换与刷新无闪烁      → 无头：三态点击 + 写记忆位 + 刷新后首帧即目标
#                              主题（rAF 首帧采 body 底色，不看过渡态，因为
#                              属性在样式表解析前已落定，根本没有过渡）；
#   ② 设置读写往返（或只读口径落实）→ 只读口径：serve 侧 settings/get 投影
#                              启动时生效的配置，面板零写入控件（无 input /
#                              无写按钮），"改配置 = 编辑 config.json 后重启"
#                              写进面板正文；本脚本同时钉住快照里的每个值。
#   ③ 既有全部面板（会话/记忆/统计/子代理/语音）两主题对比度抽查 → 无头：
#                              两主题 × 五个面 + 设置面，逐对算 WCAG 对比度。
#   ④ web-gate 过、go test ./... 绿 → 本脚本不跑这两项（重且独立），由卡内
#                              流程跑；这里只覆盖 UI 面。
#
# 干什么：临时 SEEK_HOME + 临时 config.json（serve 段刻意写非默认值，好让
# 设置面板的每一行都可断言）+ 起 `seek serve --frontend=web`；先 curl 自检
# 服务端半边（settings/get 契约、CSP 三个 sha256、页面两个主题 token 块），
# 再走无头浏览器验收（scripts/web-theme-headless.mjs）；缺 Chrome/node 时
# 打印人工清单等人按 Enter。全程不碰真实 ~/.seek 与生产 8790。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 顶栏有「⚙ 设置」；页面是暗色（缺省跟随系统）
#   2. 点⚙ 设置 → 外观三行（亮/暗/跟随系统）；点「☀ 亮」整页立即变亮，
#      刷新后仍是亮、且刷新瞬间不闪白/不闪黑
#   3. 点「🌙 暗」→ 变暗；刷新仍是暗。点「🖥 跟随系统」→ 记忆位清空，跟着
#      系统设置走（改系统外观，页面即时跟随）
#   4. 设置面板下半部：serve 侧配置（provider/模型/温度/并发/yolo/监听/
#      自动压缩/候选项目）+ 关于（版本、入口 token 后 6 位）。全是只读文本，
#      没有输入框——卡面口径：写口超配额，本轮只读展示
#   5. 两个主题下依次点开 会话/记忆/统计/后台/🎤：文字与底色都清楚
#      （会话侧栏、消息气泡、统计卡、子代理行、录音按钮）
#
# Env:
#   ADDR=127.0.0.1:18796  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1             只打印步骤不动手
#   KEEP=1                结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-theme-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18796}"   # 避开生产 8790 与其它 W5 脚本（18790-18795）

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 写 \$TMP/config.json（serve: web_addr=$ADDR, max_concurrency=6, yolo=true,"
  echo "DRY_RUN:   projects=[桩项目], default_provider=deepseek, auto_compact.at_tokens=350000）"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: curl 自检：settings/get 全字段 + token_tail=后 6 位 + 页面两主题 token 块 + CSP 3 个 sha256"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言（三态切换/刷新防闪烁/仅只读/两主题五面对比度）；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5theme-manual.XXXXXX)" || exit 1
SERVE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  if [ "${KEEP:-0}" = "1" ]; then
    # ${TMP} 花括号不可省：macOS bash 3.2 在 set -u 下把紧跟多字节字符的
    # $TMP 误读进变量名（$TMP（ → "TMP<EFBBBF>: unbound variable"）。
    echo "KEEP=1：保留 ${TMP}（serve 已停；自行清理）"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

# ---- 1. 构建 ----
echo "== build =="
(cd "$REPO" && go build -o "$TMP/seek" ./cmd/seek) || exit 1

# ---- 2. 夹具：一个候选项目 + 一条会话（面板要有东西可点开）----
echo "== fixtures =="
PROJ="$TMP/proj"
mkdir -p "$PROJ" "$TMP/sessions"
SID="20261120-100000-w5themefixture1"
printf '{"schema_version":3,"id":"%s","created_at":"2026-11-20T10:00:00+08:00","updated_at":"2026-11-20T10:05:00+08:00","model":"deepseek-chat","cwd":"%s","turns":2,"tool_calls":0,"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_cache_hit_tokens":600,"prompt_cache_miss_tokens":400}}\n' \
  "$SID" "$PROJ" > "$TMP/sessions/$SID.jsonl"
printf '{"role":"user","content":"theme fixture：请解释这段代码。\\n第二行细节。"}\n' >> "$TMP/sessions/$SID.jsonl"
printf '{"role":"assistant","content":"theme fixture：\\n```go\\nfunc main() { println(\\"ok\\") }\\n```\\n以上。"}\n' >> "$TMP/sessions/$SID.jsonl"

# ---- 3. 临时 config：serve 段刻意全非默认（否则设置面板每行都撞默认值，
#         断言就分不清「读了配置」和「读了默认」）----
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
BASE="${URL%%\?*}"; BASE="${BASE%/}"
TOK="${URL#*token=}"
TOK_TAIL="${TOK: -6}"   # 设置面板「关于」区只显示后 6 位

# ---- 5. curl 自检（服务端半边；UI 消费的同一条线）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF -e "$2" && echo yes || echo no; }
rpcc() { # rpcc <method> <params-json>
  curl -s -X POST "$BASE/rpc?token=$TOK" -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}
jget() { # jget <json> <field> — 只取顶层标量/字符串（够用的最小实现）
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p" | head -1
}

page=$(curl -s "$BASE/")
ck "GET / 带主题 token 块（暗）" "yes" "$(has "$page" '--seek-inset: #14161a')"
ck "GET / 带主题 token 块（亮）" "yes" "$(has "$page" ':root[data-theme="light"]')"
ck "GET / 带设置面板骨架" "yes" "$(has "$page" 'id="set-body"')"
ck "GET / 设置入口按钮" "yes" "$(has "$page" 'id="set-btn"')"
ck "GET / 无裸色逃逸（组件层零 hex）" "0" \
  "$(printf '%s' "$page" | sed -n '/<style>/,/<\/style>/p' | sed -n '/--seek-shadow:0 4px 14px rgba(0,0,0,.12);/,$p' | grep -cE '#[0-9a-fA-F]{6}')"

# CSP：两个内联 script（head 主题 bootstrap + 页面脚本）+ 一个内联 style
# = 3 个 sha256 令牌；head bootstrap 在样式表之前（防 FOUC 的源代码面）。
csp=$(curl -sI "$BASE/")
ck "CSP 钉 3 个内联块 sha256（head bootstrap + 页面脚本 + 样式）" "3" \
  "$(printf '%s' "$csp" | grep -o 'sha256-' | wc -l | tr -d ' ')"
ck "CSP 仍是 default-src 'self' 基线" "yes" \
  "$(printf '%s' "$csp" | grep -q "Content-Security-Policy: default-src 'self'" && echo yes || echo no)"

cfg=$(rpcc settings/get '{}')
ck "settings/get 版本在场" "yes" "$(printf '%s' "$cfg" | grep -q '"version":"[^"]' && echo yes || echo no)"
ck "settings/get token_tail = 后 6 位" "$TOK_TAIL" "$(jget "$cfg" token_tail)"
ck "settings/get 不带完整 token" "no" "$(has "$cfg" "$TOK")"
ck "settings/get default_provider=deepseek" "deepseek" "$(jget "$cfg" default_provider)"
ck "settings/get max_concurrency=6（配置值不是默认 4）" "6" "$(jget "$cfg" max_concurrency)"
ck "settings/get yolo=true" "true" "$(jget "$cfg" yolo)"
ck "settings/get web_addr=$ADDR" "$ADDR" "$(jget "$cfg" web_addr)"
ck "settings/get auto_compact 人类可读" "350000 tokens" "$(jget "$cfg" auto_compact)"
ck "settings/get 候选项目 1 条" "1" \
  "$(printf '%s' "$cfg" | grep -o "\"projects\":\[[^]]*\]" | grep -o '"/' | wc -l | tr -d ' ')"
ck "settings/get 标量 params → -32602" "yes" \
  "$(rpcc settings/get '"nope"' | grep -q '\-32602' && echo yes || echo no)"

[ "$fail" = 0 ] || { echo "自检有 FAIL——浏览器验收前先修"; exit 1; }

# ---- 6. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = "1" ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-theme-headless.mjs" "$URL" "$TOK_TAIL" "$PROJ" "$SID" \
    --chrome "$CHROME_BIN" &
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
  1. 顶栏有「⚙ 设置」；初始是暗色（无记忆位时跟随系统外观）
  2. 点⚙ → 外观「☀ 亮 / 🌙 暗 / 🖥 跟随系统」。点亮：整页立即变亮，
     刷新后仍亮，且刷新那一瞬不闪白/不闪黑（首帧就是亮）
  3. 点暗：变暗、刷新仍暗；点跟随系统：记忆位清空，改系统外观页面即时跟随
  4. 设置面板下半部「serve 侧配置（只读）」：默认 provider=deepseek、
     默认模型/温度=「无配置项」、并发上限=6、yolo=开、web 监听=$ADDR、
     自动压缩=350000 tokens、候选项目=1 条；「关于」=版本文本 + token
     …$TOK_TAIL。全页没有任何输入框（本轮只读口径）
  5. 两个主题下依次点开 记忆 / 统计 / 后台 / 会话侧栏一条 / 按住 🎤：
     文字与底色都清楚可读

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _

echo "done"
