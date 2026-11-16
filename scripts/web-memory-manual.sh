#!/usr/bin/env bash
#
# web-memory-manual.sh — W4.5 资产面 UI·二（memory 浏览器 + stats 视图）的
# 手测脚本（feature-web-frontend §9 W4.5 验收：「手测脚本 + 体积 gate 复核」；
# 体积 gate 由 scripts/web-gate.sh 承担，本脚本只管功能面）。
#
# 干什么：临时 SEEK_HOME 里造夹具——两个项目（alpha：两条记忆，其中一条
# 带 ```go 围栏正文 + 一条 stale；beta：零记忆、LastSeen 最新=缺省项目）加
# 一条带 cache 数字（600 命中/400 未命中）的会话——起 `seek serve
# --frontend=web`，先跑服务端自检（curl /rpc 的 W4.3 method 契约 + W4.5
# 增量的 cost_usd），再打印带 token 的 URL 和浏览器手测清单，等人按
# Enter 收尾清理。全程不碰真实 ~/.seek 与生产 8790（除非 ADDR 指向它，
# 别这么干）。
#
# 手测清单（脚本会原样打印）：
#   1. 「🧠 记忆」进面板：项目下拉两行、缺省落在 beta（最新）→「（这个
#      项目还没有记忆）」；切到 alpha：列表两条，📌 前缀与 ⚠ 过期标记在位
#   2. 点「zzz-keep-forever」：详情 meta（创建/更新/召回次数/📌/auto）、
#      #infra 标签章、正文代码围栏按 <pre> 渲染（textContent-only）
#   3. 「📊 统计」进面板：会话下拉缺省最新；命中率 60.0% + 双色条、
#      hit/miss 600/400、累计 tokens、成本卡片 $（服务端算的 cost_usd）
#   4. 三面板 + 聊天面互切：互斥、聊天面/SSE/会话浏览器零回归（加法面）
#
# Env:
#   ADDR=127.0.0.1:18791  监听地址（写进临时 config.json 的 serve.web_addr，
#                          只放行 loopback/tailnet 段——生产代码无 env 出口）
#   DRY_RUN=1             只打印步骤不动手（butler 自测范式）
#   KEEP=1                结束后保留临时目录与 serve 进程（人工续查）
#
# 退出码：0 自检全绿且手测完成；1 启动/自检失败；2 用法错误。
# 依赖：go、curl；浏览器（人）。
set -u

usage() {
  echo "用法：bash scripts/web-memory-manual.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"
ADDR="${ADDR:-127.0.0.1:18791}"   # 默认避开生产 8790 与 W4.4 脚本的 18790
ALPHA_ID="aa11bb22cc33dd44"
BETA_ID="ee55ff6600aabb77"
SID="20261116-100000-w45statfix01"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: 造夹具 \$TMP/projects/{$ALPHA_ID,$BETA_ID}/{manifest.json,memory.jsonl}"
  echo "DRY_RUN:   （alpha 两记忆：📌+围栏正文 / ⚠ stale；beta 零记忆、LastSeen 最新）"
  echo "DRY_RUN: 造 \$TMP/sessions/$SID.jsonl（usage 600 命中/400 未命中）"
  echo "DRY_RUN: 写 \$TMP/config.json {\"serve\":{\"web_addr\":\"$ADDR\"}}"
  echo "DRY_RUN: SEEK_HOME=\$TMP \$TMP/seek serve --frontend=web &（stderr 抓 banner URL）"
  echo "DRY_RUN: curl 自检：GET / 200+HTML（mem-btn/stat-btn）；memory/projects 新→旧；"
  echo "DRY_RUN:   memory/list 缺省=beta 空、alpha 两条；memory/get 围栏正文/未知名 -32002；"
  echo "DRY_RUN:   stats/cache hit_ratio=0.6 + cost_usd>0；缺省 sid=最新会话"
  echo "DRY_RUN: 打印手测清单 + URL，等人 Enter；清理（KEEP=1 保留）"
  exit 0
fi

TMP="$(mktemp -d /tmp/seek-w45-manual.XXXXXX)" || exit 1
SERVE_PID=""
cleanup() {
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

# ---- 2. 夹具：两个项目 + 一条带 cache 数字的会话 ----
# 项目目录名只要 16 位小写 hex（ListProjects 的容忍走查规则），manifest.json
# 才是 id/root 的权威（loadProjectReadOnly 只信 manifest）。
echo "== fixtures =="
ALPHA_DIR="$TMP/projects/$ALPHA_ID"
BETA_DIR="$TMP/projects/$BETA_ID"
mkdir -p "$ALPHA_DIR" "$BETA_DIR" "$TMP/sessions"

printf '{"schema_version":1,"project_id":"%s","abs_path":"/tmp/w45-fixture/alpha","first_seen":"2026-11-09T09:00:00+08:00","last_seen":"2026-11-10T09:00:00+08:00"}\n' \
  "$ALPHA_ID" > "$ALPHA_DIR/manifest.json"
printf '{"schema_version":1,"project_id":"%s","abs_path":"/tmp/w45-fixture/beta","first_seen":"2026-11-14T09:00:00+08:00","last_seen":"2026-11-15T09:00:00+08:00"}\n' \
  "$BETA_ID" > "$BETA_DIR/manifest.json"

# alpha 的两条记忆：📌 常驻（正文带 ```go 围栏）+ ⚠ stale 待回收。
# JSONL 一行一条 Entry（W4.3 测试夹具 writeProjectFixture 同构）。
{
  printf '{"schema_version":1,"name":"zzz-keep-forever","tagline":"load-bearing","content":"the body with\\ntwo lines\\n```go\\nfunc main() { println(\\"ok\\") }\\n```","tags":["infra"],"created_at":"2026-11-01T09:00:00+08:00","updated_at":"2026-11-10T09:00:00+08:00","last_recalled_at":"2026-11-10T09:00:00+08:00","recall_count":7,"pinned":true,"auto_sourced":true}\n'
  printf '{"schema_version":1,"name":"aaa-old-decision","tagline":"first take","content":"superseded rationale","created_at":"2026-11-01T09:00:00+08:00","updated_at":"2026-11-08T09:00:00+08:00","last_recalled_at":"2026-11-08T09:00:00+08:00","recall_count":1,"stale":true}\n'
} > "$ALPHA_DIR/memory.jsonl"

# 会话头行 = SaveTo line 1（schema_version 2）；usage 600 命中/400 未命中
# → stats/cache 的 hit_ratio 0.6、cost_usd > 0 都有得看。
printf '{"schema_version":2,"id":"%s","created_at":"2026-11-16T10:00:00+08:00","updated_at":"2026-11-16T11:00:00+08:00","model":"deepseek-chat","cwd":"/tmp/w45-fixture/alpha","turns":2,"tool_calls":0,"usage":{"prompt_tokens":1000,"completion_tokens":200,"total_tokens":1200,"prompt_cache_hit_tokens":600,"prompt_cache_miss_tokens":400}}\n' \
  "$SID" > "$TMP/sessions/$SID.jsonl"
printf '{"role":"user","content":"w45 fixture：你好"}\n' >> "$TMP/sessions/$SID.jsonl"
printf '{"role":"assistant","content":"w45 fixture：你好，有什么可以帮你？"}\n' >> "$TMP/sessions/$SID.jsonl"
wc -l "$ALPHA_DIR/memory.jsonl" "$TMP/sessions/$SID.jsonl"

# ---- 3. 临时 config：serve.web_addr（生产代码无 env 监听出口，走 config 通道）----
printf '{"serve":{"web_addr":"%s"}}\n' "$ADDR" > "$TMP/config.json"

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

# ---- 5. 服务端自检（/rpc 契约 = UI 消费的同一条路）----
echo "== self-checks =="
fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want $2 got $3"; fail=1; fi
}
rpcc() { # rpcc <method> <params-json> → 响应体（banner URL 去查询串后拼 /rpc）
  curl -s -X POST "${URL%%\?*}rpc?token=${URL#*token=}" \
    -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"$1\",\"params\":$2}"
}
first_proj() { printf '%s' "$1" | grep -o '"project_id":"[0-9a-f]*"' | head -1; }
entry_count() { printf '%s' "$1" | grep -o '"name"' | wc -l | tr -d ' '; }

html=$(curl -s "${URL%%\?*}")
ck "GET / 200+页面（记忆入口）" "yes" "$(printf '%s' "$html" | grep -q 'id="mem-btn"' && echo yes || echo no)"
ck "GET / 200+页面（统计入口）" "yes" "$(printf '%s' "$html" | grep -q 'id="stat-btn"' && echo yes || echo no)"

projs=$(rpcc memory/projects '{}')
ck "memory/projects 新→旧（beta 先）" "\"project_id\":\"$BETA_ID\"" "$(first_proj "$projs")"
ck "memory/projects 含 alpha" "yes" "$(printf '%s' "$projs" | grep -q "$ALPHA_ID" && echo yes || echo no)"

# 缺省 project_id = 最新 LastSeen = beta → 空列表（不是错误）
dflt=$(rpcc memory/list '{}')
ck "memory/list 缺省=beta 空" "0" "$(entry_count "$dflt")"

alpha=$(rpcc memory/list "{\"project_id\":\"$ALPHA_ID\"}")
ck "memory/list alpha 两条" "2" "$(entry_count "$alpha")"
ck "alpha 含 📌 常驻条目" "yes" "$(printf '%s' "$alpha" | grep -q '"pinned":true' && echo yes || echo no)"
ck "alpha 含 ⚠ stale 标记" "yes" "$(printf '%s' "$alpha" | grep -q '"stale":true' && echo yes || echo no)"

got=$(rpcc memory/get "{\"project_id\":\"$ALPHA_ID\",\"name\":\"zzz-keep-forever\"}")
ck "memory/get 围栏正文" "yes" "$(printf '%s' "$got" | grep -q 'func main' && echo yes || echo no)"
ck "memory/get 召回计数" "yes" "$(printf '%s' "$got" | grep -q '"recall_count":7' && echo yes || echo no)"

miss=$(rpcc memory/get "{\"project_id\":\"$ALPHA_ID\",\"name\":\"nope\"}")
ck "memory/get 未知名 -32002" "yes" "$(printf '%s' "$miss" | grep -q '"code":-32002' && echo yes || echo no)"

stat=$(rpcc stats/cache "{\"sid\":\"$SID\"}")
ck "stats/cache hit_ratio 0.6" "yes" "$(printf '%s' "$stat" | grep -q '"hit_ratio":0.6' && echo yes || echo no)"
ck "stats/cache cost_usd 在场" "yes" "$(printf '%s' "$stat" | grep -q '"cost_usd":' && echo yes || echo no)"
ck "stats/cache cost_usd>0" "yes" "$(printf '%s' "$stat" | grep -qE '"cost_usd":0\.0*[1-9]' && echo yes || echo no)"

dstat=$(rpcc stats/cache '{}')
ck "stats/cache 缺省=最新会话" "yes" "$(printf '%s' "$dstat" | grep -q "\"sid\":\"$SID\"" && echo yes || echo no)"

[ "$fail" = 0 ] || { echo "自检有 FAIL——手测前先修"; exit 1; }

# ---- 6. 手测清单 + 等人 ----
cat <<EOF

== 手测清单（浏览器）==
  打开：$URL
  1. 点「🧠 记忆」→ 项目下拉两行、缺省 beta（最新）→ 列表「（这个项目还没有
     记忆）」；下拉切 alpha：两条，「zzz-keep-forever」带 📌 前缀、
     「aaa-old-decision」带 ⚠ 过期 · 字样
  2. 点「zzz-keep-forever」：meta 行（建于/更新/召回 7 次/📌 常驻/auto）、
     #infra 标签章、正文里 go 代码围栏成 <pre> 块（与聊天面同一渲染器）
  3. 点「📊 统计」→ 会话下拉缺省 w45 fixture：命中率 60.0% + 绿色进度条、
     命中/未命中 600/400、累计 tokens（1,000/200/1,200）、成本卡片 \$（服务端
     cost_usd，页面不携带费率）
  4. 「📚 会话」↔「🧠 记忆」↔「📊 统计」↔「💬 聊天」互切：一次只亮一个面板；
     会话浏览器与聊天面（输入框/状态行/SSE）原样（W4.5 是加法，零回归面）
  5. 模型输出渲染安全目视：详情正文只出纯文本/<pre>，无任何 HTML 解释迹象

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
read -r _

echo "done"
