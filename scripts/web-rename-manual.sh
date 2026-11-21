#!/usr/bin/env bash
#
# web-rename-manual.sh — W5 卡 c_rqcuaa（会话重命名与归档）的验收脚本。
#
# 干什么：临时 SEEK_HOME 里造两条会话夹具（一条改名用、一条归档用），
# 起 `seek serve --frontend=web`，先跑服务端自检（curl /rpc 的
# rename/archive/list 契约 + 幂等 + 审计行 + 无权限 403），再走浏览器
# 验收：
#   - 本机有 Chrome + node≥22 → scripts/web-rename-headless.mjs 无头跑
#     全部断言（改名→列表刷新 / Esc 取消 / 归档→隐藏→切查看可见→
#     取消归档 / 切换位刷新保持）；
#   - 缺任一 → 打印人工清单等人按 Enter。
# 全程不碰真实 ~/.seek 与生产 8790（除非 ADDR 指向它，别这么干）。
#
# 手测清单（无头不可用时脚本原样打印）：
#   1. 打开 URL → 侧栏两条夹具；悬停行右缘出 ✏/🗄 两钮
#   2. 点 ✏ → 行变输入框（预填旧标题）；改字回车 → 整表刷新，新标题在；
#      再改一次按 Esc → 标题不变
#   3. 点 🗄 → 该行消失；点头部 🗄 → 该行回来（变淡 + 「已归档 ·」前缀 +
#      ⤴ 钮）；点 ⤴ → 恢复正常行
#   4. 归档可见位开着时刷新页面 → 仍开着（localStorage）
#   5. 改名/归档失败路径（超长标题）→ 侧栏脚出错误提示，列表不坏
#
# Env:
#   ADDR=127.0.0.1:18795  监听地址（写进临时 config.json 的 serve.web_addr）
#   CHROME=<路径>          无头浏览器可执行文件（默认 macOS 标准位）
#   DRY_RUN=1             只打印步骤不动手（butler 自测范式）
#   KEEP=1                结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检+无头断言全绿（或人工清单走完）；1 失败；2 用法错误。
# 依赖：go、curl；无头验收另需 Chrome 与 node≥22（缺则退人工清单）。
set -u

usage() {
  echo "用法：bash scripts/web-rename-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18795}"
REN_SID="20261120-100000-w5renfix000000001"
ARC_SID="20261120-100001-w5arcfix000000001"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 造夹具 \$TMP/sessions/{$REN_SID,$ARC_SID}.jsonl（各 2 条消息）"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（stderr 抓 banner URL）"
  echo "DRY_RUN: curl 自检：rename 落库+list 覆盖；空标题/游走 sid 拒；archive 隐藏/幂等/unarchive；审计行；cookie-only 403"
  echo "DRY_RUN: 浏览器：Chrome+node 在 → 无头断言；缺 → 人工清单等 Enter；清理"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w5ren-manual.XXXXXX)" || exit 1
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

# ---- 2. 夹具：两条会话（头行契约 = SaveTo 的 line 1）----
echo "== fixtures =="
mkdir -p "$TMP/sessions"
hdr() { # hdr <id> <created> <updated>
  printf '{"schema_version":2,"id":"%s","created_at":"%s","updated_at":"%s","model":"deepseek-chat","cwd":"/tmp/w5ren-fixture","turns":1,"tool_calls":0,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":10}}\n' \
    "$1" "$2" "$3"
}
mk() { # mk <id> <created> <updated> <首条用户消息>
  local f="$TMP/sessions/$1.jsonl"
  hdr "$1" "$2" "$3" > "$f"
  printf '{"role":"user","content":"%s"}\n' "$4" >> "$f"
  printf '{"role":"assistant","content":"ok"}\n' >> "$f"
}
mk "$REN_SID" "2026-11-20T10:00:00+08:00" "2026-11-20T10:05:00+08:00" "rename fixture：原始标题"
mk "$ARC_SID" "2026-11-20T09:00:00+08:00" "2026-11-20T09:05:00+08:00" "archive fixture：待归档"

# ---- 3. 临时 config + 起 serve，抓 banner URL ----
printf '{"serve":{"web_addr":"%s"}}\n' "$ADDR" > "$TMP/config.json"
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
# getcode：提取 JSON-RPC 错误码（全为 -32xxx 负数；macOS sed 无 \?，走 grep -o）
getcode() { printf '%s' "$1" | grep -o '"code":-[0-9]*' | head -1 | cut -d: -f2; }

r=$(rpcc session/rename "{\"sid\":\"$REN_SID\",\"title\":\"服务端改名\"}")
ck "rename 结果带新标题" "yes" "$(printf '%s' "$r" | grep -q '"title":"服务端改名"' && echo yes || echo no)"
list=$(rpcc session/list '{}')
ck "list 标题被覆盖" "yes" "$(printf '%s' "$list" | grep -q '服务端改名' && echo yes || echo no)"
ck "list 旧派生标题退位" "no" "$(printf '%s' "$list" | grep -q '原始标题' && echo yes || echo no)"
ck "空标题 -32602" "-32602" "$(getcode "$(rpcc session/rename '{"sid":"'$REN_SID'","title":"  "}')")"
ck "游走 sid -32602" "-32602" "$(getcode "$(rpcc session/rename '{"sid":"../evil","title":"x"}')")"
ck "缺失会话 rename -32001" "-32001" "$(getcode "$(rpcc session/rename '{"sid":"20990101-000000-nosuchsession00","title":"x"}')")"

r=$(rpcc session/archive "{\"sid\":\"$ARC_SID\"}")
ck "首次归档 changed=true" "yes" "$(printf '%s' "$r" | grep -q '"changed":true' && echo yes || echo no)"
list=$(rpcc session/list '{}')
ck "默认列表隐藏归档行" "no" "$(printf '%s' "$list" | grep -q "$ARC_SID" && echo yes || echo no)"
list=$(rpcc session/list '{"include_archived":true}')
ck "include_archived 可见且带标" "yes" "$(printf '%s' "$list" | grep -q "\"sid\":\"$ARC_SID\"" && printf '%s' "$list" | grep -q '"archived":true' && echo yes || echo no)"
r=$(rpcc session/archive "{\"sid\":\"$ARC_SID\",\"archived\":true}")
ck "二次归档幂等 changed=false" "yes" "$(printf '%s' "$r" | grep -q '"changed":false' && echo yes || echo no)"
r=$(rpcc session/archive "{\"sid\":\"$ARC_SID\",\"archived\":false}")
ck "取消归档 changed=true" "yes" "$(printf '%s' "$r" | grep -q '"archived":false,"changed":true' && echo yes || echo no)"
list=$(rpcc session/list '{}')
ck "取消归档后回默认列表" "yes" "$(printf '%s' "$list" | grep -q "$ARC_SID" && echo yes || echo no)"

AUDIT="$TMP/serve/sessions-meta-audit.jsonl"
ck "审计行落在 serve 目录" "yes" "$([ -s "$AUDIT" ] && echo yes || echo no)"
ck "审计含 rename/archive/unarchive" "yes" "$(grep -q '"event":"rename"' "$AUDIT" && grep -q '"event":"archive"' "$AUDIT" && grep -q '"event":"unarchive"' "$AUDIT" && echo yes || echo no)"

# 无权限（c_rqcuaa 验收①）：cookie 单独持有（无 Origin/无 X-Seek-Token）→ 403
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}rpc" \
  -H 'Content-Type: application/json' -H "Cookie: seek_web_token=$TOK" \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/rename\",\"params\":{\"sid\":\"$REN_SID\",\"title\":\"越权\"}}")
ck "cookie-only rename 403" "403" "$code"
list=$(rpcc session/list '{}')
ck "被拒改名未落库" "no" "$(printf '%s' "$list" | grep -q '越权' && echo yes || echo no)"

# 自检收尾：把改名夹具改回原题——浏览器景的基线断言（编辑态预填「原始标题」）
# 依赖它；这也是本脚本里第三次 rename 往返，顺带再证一次落库。
r=$(rpcc session/rename "{\"sid\":\"$REN_SID\",\"title\":\"rename fixture：原始标题\"}")
ck "自检收尾改回原题" "yes" "$(printf '%s' "$r" | grep -q '"title":"rename fixture：原始标题"' && echo yes || echo no)"

[ "$fail" = 0 ] || { echo "自检有 FAIL——手测前先修"; exit 1; }

# ---- 5. 浏览器验收：无头或人工 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = "1" ]; then
  echo "== headless（Chrome + node CDP）=="
  "$NODE_BIN" "$HERE/web-rename-headless.mjs" "$URL" "$REN_SID" "$ARC_SID" \
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
# 与 web-sessions-manual 同一口径（c_kwiajf / docs/pitfalls/tooling
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
echo "1. 打开 $URL"
echo "2. 侧栏悬停 $REN_SID 行右缘 → ✏/🗄 两钮"
echo "3. 点 ✏ 改名回车 → 刷新后新标题；再改按 Esc → 不变"
echo "4. 点 🗄 → 行消失；头部 🗄 → 行回来（变淡+已归档前缀+⤴）；⤴ → 恢复"
echo "5. 归档可见位开着刷新 → 仍开"
echo "按 Enter 收尾（清理现场）…"
read _
exit 0
