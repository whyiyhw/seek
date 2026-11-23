#!/usr/bin/env bash
#
# web-page-review.sh — seek web 页面的无头截图审查入口（卡 c_vcnacz，
# 2026-11-22 一次性沙盒截图的仓内沉淀）。背景：seek web 页面烂了近一周
# 无人发现，根因是没有定时视觉审查；本脚本把它变成可复跑入口，供 cron
# 定时挑刺（butler page-review 扩容卡）与人工复跑。
#
# 干什么（沿 web-*-manual.sh 家族模式，shell 编排 + .mjs 无头驱动）：
#   1. 临时 SEEK_HOME + 富夹具：3 条会话（富会话含 GFM 表格/引用块/长行
#      折行/超长路径与 hash/工具树行等已知易烂元素）、记忆 3 条、
#      hooks 审计 5 行、子代理事件 3 条；
#   2. go build + `serve --frontend=web` 起沙盒（随机高端口 28000–42000，
#      实测空闲才用，避开生产 8790 与既有手测脚本的 187xx/188xx 段）；
#   3. scripts/web-page-review-headless.mjs 走无头 Chrome 截图矩阵：
#      亮/暗 × 1440 桌面/390 手机宽 × 8 面板（聊天空态/侧栏/富会话/记忆/
#      统计/后台/设置/审计）= 32 张 PNG + console 收集 + 富会话 DOM dump；
#   4. 产物落证据目录（manifest + 逐镜头 PNG + console.txt +
#      dom-chat-rich.html + isolation.txt 沙盒自证）；
#   5. 沙盒自证：跑前落时间戳戳 file，跑后 `find ~/.seek -newer <戳>`
#      必须为空（真实 ~/.seek 零写入）；banner 端口 ≠ 8790。
#
# 手测清单（Chrome/node 缺失时脚本原样打印）：
#   打开脚本打印的 URL → /bind <脚本打印的桩项目路径> → 逐面板
#   （侧栏/富会话/记忆/统计/后台/设置/审计）× 亮暗 × 桌面/窄窗逐屏
#   截图存入 OUT_DIR——矩阵同无头半边的 8 面板口径。
#
# Env:
#   OUT_DIR=<目录>        证据目录（默认 <repo>/out/page-review-<时间戳>；
#                         out/ 已 gitignore，不进版本库）
#   CHROME=<路径>         无头浏览器可执行文件（默认 macOS 标准位）
#   PORT=<端口>           显式指定监听端口（默认随机 28000–42000 实测空闲）
#   DRY_RUN=1             只打印步骤不动手
#   KEEP=1                结束后保留沙盒临时目录（serve 已停；自行清理）
#
# 退出码：0 截图矩阵拍齐（console/面板异常是审查证据，不算失败）；1 失败；
#         2 用法错误；3 无头条件缺且 stdin 非 TTY（无人值守车道：报环境
#         能力缺，不假装绿）。
# 依赖：go、curl；无头审查另需 Chrome 与 node≥22（缺则退人工清单）。
# 机制预算：一次性脚本沉淀，无新增常驻机制。
set -u

usage() {
  echo "用法：bash scripts/web-page-review.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "$HERE")"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: 选随机空闲高端口（28000–42000，避开 8790/187xx/188xx）"
  echo "DRY_RUN: go build ./cmd/seek → \$TMP/seek"
  echo "DRY_RUN: \$TMP/seekhome 夹具（3 会话/3 记忆/5 审计行/3 子代理事件）+ \$TMP/proj 桩项目"
  echo "DRY_RUN: 写 \$TMP/seekhome/config.json {\"serve\":{\"web_addr\":\"127.0.0.1:\$PORT\"}}"
  echo "DRY_RUN: SEEK_HOME=\$TMP/seekhome \$TMP/seek serve --frontend=web &（抓 banner）"
  echo "DRY_RUN: node scripts/web-page-review-headless.mjs：亮/暗 × 1440/390 × 8 面板 = 32 PNG + console + DOM dump → \$OUT_DIR"
  echo "DRY_RUN: 沙盒自证：find ~/.seek -newer <跑前戳> 为空 + 端口 ≠ 8790 → \$OUT_DIR/isolation.txt"
  echo "DRY_RUN: 清理（KEEP=1 保留沙盒）"
  exit 0
fi

# ---- 0. 证据目录 + 沙盒自证的跑前戳 ----
OUT_DIR="${OUT_DIR:-$REPO/out/page-review-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT_DIR" || exit 1
REAL_SEEK="$HOME/.seek"
STAMP="$(mktemp "${TMPDIR:-/tmp}/seek-review-stamp.XXXXXX")" || exit 1
touch "$STAMP"

# ---- 1. 随机空闲高端口（避开生产 8790 与既有手测 18790–18797、CDP 18850）----
free_port() {
  local p i
  for i in $(seq 1 40); do
    p=$(( (RANDOM % 14000) + 28000 ))
    case "$p" in 8790|1879[0-9]|1885[0-9]) continue ;; esac
    if (exec 3<>"/dev/tcp/127.0.0.1/$p") 2>/dev/null; then
      exec 3>&- 3<&-   # 已被占，接着掷
    else
      printf '%s' "$p"; return 0
    fi
  done
  return 1
}
if [ -n "${PORT:-}" ]; then
  ADDR="127.0.0.1:${PORT}"
else
  P="$(free_port)" || { echo "FAIL: 40 次掷端口无空闲"; exit 1; }
  ADDR="127.0.0.1:${P}"
fi
case "$ADDR" in
  *:8790) echo "FAIL: 拒绝使用生产端口 8790"; exit 1 ;;
esac

TMP="$(mktemp -d "${TMPDIR:-/tmp}/seek-page-review.XXXXXX")" || exit 1
HOME_DIR="$TMP/seekhome"
PROJ="$TMP/proj"
SERVE_PID=""
NODE_PID=""
cleanup() {
  [ -n "$NODE_PID" ] && kill "$NODE_PID" 2>/dev/null
  [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null && wait "$SERVE_PID" 2>/dev/null
  rm -f "$STAMP"
  if [ "${KEEP:-0}" = "1" ]; then
    echo "KEEP=1：保留沙盒 ${TMP}（serve 已停；自行清理）。证据在 ${OUT_DIR}"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

# ---- 2. 构建 ----
echo "== build =="
(cd "$REPO" && go build -o "$TMP/seek" ./cmd/seek) || exit 1

# ---- 3. 夹具：临时 SEEK_HOME + 桩项目 ----
# 会话/记忆/审计/子代理的盘上格式与 internal 测试夹具同源
# （internal/rpc/methods/*_test.go 的 writeProjectFixture 等）。
echo "== fixtures =="
RICH_SID="20261122-094500-aaaarichfixt0001"
SID2="20261122-091500-bbbbprobe000002"
SID3="20261122-090000-ccccthird000003"
mkdir -p "$HOME_DIR/sessions" "$PROJ"

# 项目 id = sha256(绝对路径)[:16]（paths.ProjectID 契约；/tmp 在 macOS 是
# /private/tmp 的符号链接，mktemp 返回物理路径，两侧同源）。
hash16() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$1" | sha256sum
  else
    printf '%s' "$1" | shasum -a 256
  fi | cut -c1-16
}
PROJ_ID="$(hash16 "$PROJ")"
mkdir -p "$HOME_DIR/projects/$PROJ_ID"

# 富会话：易烂元素清单（卡面 ⑥）全数入镜——GFM 表格、引用块、长行折行、
# 超长路径、64 位 hash、工具树行（tool_calls + tool 结果成对）。
cat > "$HOME_DIR/sessions/$RICH_SID.jsonl" <<EOF
{"schema_version":3,"id":"$RICH_SID","created_at":"2026-11-22T09:30:00+08:00","updated_at":"2026-11-22T09:45:00+08:00","model":"deepseek-chat","cwd":"$PROJ","turns":4,"tool_calls":3,"usage":{"prompt_tokens":21000,"completion_tokens":1500,"prompt_cache_hit_tokens":16000,"prompt_cache_miss_tokens":5000,"total_tokens":22500}}
{"role":"user","content":"帮我把仓库里的超长路径文件梳理一下：/Users/demo/work/clients/acme/trunk/modules/billing/invoice/legacy-v2/imports/very/deeply/nested/path/segments/that/never/seem/to/end/2024/q4/final/final-v3-really-final.tar.gz"}
{"role":"assistant","content":"","tool_calls":[{"id":"call_rich_grep01","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"TODO|FIXME\",\"path\":\"/Users/demo/work/clients/acme/trunk/modules/billing\",\"max_matches\":20}"}}]}
{"role":"tool","tool_call_id":"call_rich_grep01","content":"internal/billing/invoice.go:88: TODO: 后端迁移后删掉这段兼容分支 3f2a9c1d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9\ninternal/billing/invoice_test.go:12: FIXME: 表格驱动用例缺「零税率」行 9d8c7b6a5f4e3d2c1b0a998877665544332211fedcba9876543210\n"}
{"role":"assistant","content":"梳理如下。\n\n## 超长路径与待办分布\n\n| 模块 | 待办数 | 最近提交 |\n| --- | ---: | --- |\n| billing/invoice | 2 | 3f2a9c1d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9 |\n| billing/refund | 1 | 9d8c7b6a5f4e3d2c1b0a998877665544332211fedcba9876543210 |\n\n> 引用块：这两处 TODO 都挂在 2024 Q4 的迁移分支上，负责人已离职。\n\n长行折行检查——这一行没有任何换行机会却必须被容器宽度强制折行：LoooooooooooooooooooooooooooooooooooooooooooooooongIdentifierName + AnotherExtremelyLongConstantName_With_Underscores_And_More_Words_To_Force_Wrapping = 12345678901234567890。\n\n行内要点：\`grep -rn TODO\` 起手，[迁移清单](https://example.com/migration) 收尾。"}
{"role":"user","content":"收到，退款模块那个也看下"}
{"role":"assistant","content":"退款模块只有一处 FIXME（表格驱动用例缺「零税率」行），其余干净。"}
EOF

cat > "$HOME_DIR/sessions/$SID2.jsonl" <<EOF
{"schema_version":3,"id":"$SID2","created_at":"2026-11-22T09:10:00+08:00","updated_at":"2026-11-22T09:15:00+08:00","model":"deepseek-reasoner","cwd":"$PROJ","turns":1,"tool_calls":0,"usage":{"prompt_tokens":3000,"completion_tokens":200,"prompt_cache_hit_tokens":1000,"prompt_cache_miss_tokens":2000,"total_tokens":3200}}
{"role":"user","content":"记一下：这个项目的测试命令是 go test -race ./..."}
{"role":"assistant","reasoning_content":"用户要求记录项目事实，直接确认。","content":"已记下：go test -race ./..."}
EOF

cat > "$HOME_DIR/sessions/$SID3.jsonl" <<EOF
{"schema_version":3,"id":"$SID3","created_at":"2026-11-22T09:00:00+08:00","updated_at":"2026-11-22T09:05:00+08:00","model":"deepseek-chat","cwd":"$PROJ","turns":2,"tool_calls":1,"usage":{"prompt_tokens":5000,"completion_tokens":300,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":5000,"total_tokens":5300}}
{"role":"user","content":"/plan 分析一下构建为何变慢"}
{"role":"assistant","content":"","tool_calls":[{"id":"call_third_bash1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"go build -t -x ./... 2>\\u00261 | tail -20\"}"}}]}
{"role":"tool","tool_call_id":"call_third_bash1","content":"（构建耗时 42s，其中链接 31s）"}
{"role":"assistant","content":"构建慢的主因在链接段（31s/42s）。"}
EOF

# 记忆 3 条：pinned / stale / 普通，覆盖面板三态渲染。
cat > "$HOME_DIR/projects/$PROJ_ID/memory.jsonl" <<'EOF'
{"schema_version":1,"name":"build-commands","tagline":"构建与测试命令速查","content":"构建：go build ./cmd/seek\n全量测试：go test -race ./...\n体积闸：scripts/web-gate.sh","tags":["go","ci"],"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-20T10:00:00Z","last_recalled_at":"2026-11-21T09:00:00Z","recall_count":12,"pinned":true}
{"schema_version":1,"name":"legacy-invoice-migration","tagline":"Q4 发票迁移分支的坑","content":"invoice.go:88 的兼容分支挂在已离职负责人的迁移清单上，动之前先看 https://example.com/migration","tags":["billing"],"created_at":"2026-09-01T10:00:00Z","updated_at":"2026-10-01T10:00:00Z","last_recalled_at":"2026-10-02T09:00:00Z","recall_count":3,"stale":true,"stale_since":"2026-11-01T00:00:00Z"}
{"schema_version":1,"name":"release-notes-style","tagline":"发版说明的固定格式","content":"三段式：变更摘要 / 迁移注意 / 已知问题。中文为主，命令与代码保持原文。","created_at":"2026-11-05T10:00:00Z","updated_at":"2026-11-15T10:00:00Z","last_recalled_at":"2026-11-18T09:00:00Z","recall_count":5}
EOF
printf '{"schema_version":1,"project_id":"%s","abs_path":"%s","first_seen":"2026-09-01T09:00:00Z","last_seen":"2026-11-22T09:45:00Z"}\n' \
  "$PROJ_ID" "$PROJ" > "$HOME_DIR/projects/$PROJ_ID/manifest.json"

# 子代理事件 3 条：completed（带 tokens）/ failed（带 reason）——后台面板
# 的状态徽标两态 + tokens 行 + 末条输出字段都有料。
cat > "$HOME_DIR/projects/$PROJ_ID/subagents.jsonl" <<EOF
{"event":"started","sub_sid":"20261122-093101-subfixt00000001","ts":"2026-11-22T09:31:01+08:00","parent_sid":"$RICH_SID","parent_turn":2,"type":"explore","description":"盘点超长路径下的 TODO 分布"}
{"event":"completed","sub_sid":"20261122-093101-subfixt00000001","ts":"2026-11-22T09:32:40+08:00","tokens":{"prompt":8000,"completion":600,"cache_hit":4000}}
{"event":"failed","sub_sid":"20261122-094002-subfixt00000002","ts":"2026-11-22T09:40:30+08:00","parent_sid":"$RICH_SID","parent_turn":3,"type":"explore","description":"核对退款模块用例","reason":"API 配额用尽"}
EOF

# hooks 审计 5 行：普通执行 / 拒绝 / 注入 / 超时，审计面板四种行态。
cat > "$HOME_DIR/hooks-audit.jsonl" <<EOF
{"ts":"2026-11-22T09:31:10+08:00","event":"post_tool","hook":"acme-guard","tool":"bash","session_id":"$RICH_SID","duration_ms":120,"exit_code":0,"denied":false}
{"ts":"2026-11-22T09:31:20+08:00","event":"pre_tool","hook":"no-rm-rf","tool":"bash","session_id":"$RICH_SID","duration_ms":8,"exit_code":1,"denied":true,"reason":"命令含 rm -rf，命中静态跳过规则"}
{"ts":"2026-11-22T09:31:30+08:00","event":"pre_prompt_inject","hook":"project-notes","session_id":"$RICH_SID","duration_ms":15,"exit_code":0,"denied":false,"injected_bytes":420}
{"ts":"2026-11-22T09:32:00+08:00","event":"post_tool","hook":"notify","tool":"write","session_id":"$SID2","duration_ms":60050,"exit_code":124,"denied":false,"reason":"通知脚本超时"}
{"ts":"2026-11-22T09:39:00+08:00","event":"pre_tool","hook":"acme-guard","tool":"grep","session_id":"$SID3","duration_ms":5,"exit_code":0,"denied":false}
EOF

# serve 配置：随机端口 + 桩项目进候选（设置面板的 projects 行有料）。
printf '{"serve":{"web_addr":"%s","projects":["%s"],"max_concurrency":2,"yolo":false}}\n' \
  "$ADDR" "$PROJ" > "$HOME_DIR/config.json"

printf '{\n  "sessions": ["%s", "%s", "%s"],\n  "rich_sid": "%s",\n  "memory_entries": 3,\n  "hooks_audit_lines": 5,\n  "subagent_events": 3,\n  "project_id": "%s"\n}\n' \
  "$RICH_SID" "$SID2" "$SID3" "$RICH_SID" "$PROJ_ID" > "$OUT_DIR/fixtures.json"

# ---- 4. 起沙盒 serve ----
echo "== serve up（${ADDR}）=="
: > "$TMP/serve.log"
SEEK_HOME="$HOME_DIR" "$TMP/seek" serve --frontend=web 2> "$TMP/serve.log" &
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
BASE="${URL%%\?*}"; BASE="${BASE%/}"

# ---- 5. 服务端自检（页面能开再进浏览器）----
echo "== self-check =="
page_code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE")
if [ "$page_code" != "200" ]; then
  echo "FAIL: GET / = ${page_code}（serve.log 尾部）："; tail -5 "$TMP/serve.log"; exit 1
fi
echo "  ok  GET / 200（沙盒页面可开，${ADDR}）"

# ---- 6. 无头截图矩阵，或人工清单 ----
CHROME_BIN="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
NODE_BIN="$(command -v node || true)"
HEADLESS=1
[ -x "$CHROME_BIN" ] || HEADLESS=0
[ -n "$NODE_BIN" ] || HEADLESS=0
if [ "$HEADLESS" = 1 ]; then
  echo "== headless 矩阵（亮/暗 × 1440/390 × 8 面板 = 32 镜头 → ${OUT_DIR}）=="
  PAGE_REVIEW_HOME="$HOME_DIR" PAGE_REVIEW_GIT_REV="$(git -C "$REPO" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
    "$NODE_BIN" "$HERE/web-page-review-headless.mjs" "$URL" "$PROJ" "$OUT_DIR" \
    --rich-sid "$RICH_SID" --chrome "$CHROME_BIN" --dbg-port 18851 &
  NODE_PID=$!
  wait "$NODE_PID"; rc=$?
  NODE_PID=""
  [ "$rc" = 0 ] || { echo "FAIL: 无头驱动退出 $rc"; exit 1; }
else
  # 无头条件缺 + 非 TTY = 无人值守车道：人工清单无处可走（c_kwiajf 判据，
  # web-md-manual.sh 同款退 3）。
  if [ ! -t 0 ]; then
    miss=""
    [ -x "$CHROME_BIN" ] || miss="Chrome（${CHROME_BIN}）"
    [ -n "$NODE_BIN" ] || miss="${miss:+${miss} + }node"
    echo "FAIL: 无头审查条件缺（${miss}）且 stdin 非 TTY——无人值守没有人工清单可走。装上缺的组件或 CHROME=<路径> 重跑。" >&2
    exit 3
  fi
  cat <<EOF

== 无头条件缺（Chrome/node）——人工审查清单 ==
  打开：$URL
  1. /bind $PROJ → 收「已绑定项目 …」
  2. 逐面板截图存入 ${OUT_DIR}（亮/暗 × 1440/390 窄窗）：
     侧栏（会话列表）、富会话（${RICH_SID}，展开一行工具树）、
     🧠 记忆、📊 统计、🤖 后台、⚙ 设置、🪝 审计
  3. DevTools console 全程开着，报错抄进 $OUT_DIR/console.txt

按 Enter 结束并清理（KEEP=1 保留现场）…
EOF
  read -r _
fi

# ---- 7. 沙盒自证 + 产物清点 ----
# 归属式两条硬证据 + 一条参考线（2026-11-22 首跑实测教训：裸 `find
# ~/.seek -newer` 全目录扫在多 agent 共机上必误伤——外来 seek 车道恰在
# 本窗口内追加自己的会话文件，命中≠本脚本所写）：
#   硬 1  端口：banner addr ≠ 8790（起服时已断言）；
#   硬 2  进程环境：serve 进程 ps eww 里 SEEK_HOME=沙盒路径——本脚本的
#         进程只会往沙盒写（env 钉死，子进程继承）；
#   硬 3  文件句柄：serve 进程树 lsof 无任何真实 ~/.seek 路径被打开；
#   参考  find ~/.seek -newer <跑前戳>：窗口内变更清单（可能混入外来
#         车道的写入），只作线索不定罪。
TREE_PIDS="$SERVE_PID $(pgrep -P "$SERVE_PID" 2>/dev/null | tr '\n' ' ')"
PID_LIST="$(printf '%s' "$TREE_PIDS" | tr ' ' ',' | sed 's/,$//')"
SERVE_ENV_HITS=$(ps eww -p "$SERVE_PID" -o command= 2>/dev/null | grep -cF "SEEK_HOME=$HOME_DIR")
FD_HITS=$(lsof -fnP -p "$PID_LIST" 2>/dev/null | grep -cF "$REAL_SEEK/")
{
  echo "sandbox self-proof（卡 c_vcnacz 验收 ③；归属式证据）"
  echo "  serve_addr:       ${ADDR}（生产 8790 未用）"
  echo "  seek_home:        ${HOME_DIR}（临时沙盒；真实 ~/.seek=${REAL_SEEK}）"
  echo "  serve_env_seek_home: ${SERVE_ENV_HITS} 处命中（≥1 = serve 进程环境钉死沙盒）"
  echo "  serve_tree_pids:  ${TREE_PIDS}"
  echo "  serve_tree_fds_on_real_seek: ${FD_HITS}（0 = 进程树没打开真实 ~/.seek 下任何文件）"
  if [ -d "$REAL_SEEK" ]; then
    CHANGED="$(find "$REAL_SEEK" -newer "$STAMP" 2>/dev/null | head -5)"
    if [ -z "$CHANGED" ]; then
      echo "  window_changes:   无（find ~/.seek -newer <跑前戳> 为空）"
    else
      echo "  window_changes:   有（参考线——共机上可能是外来 seek 车道写的，归属以上面两条硬证据为准）："
      printf '    %s\n' $CHANGED
    fi
  else
    echo "  window_changes:   无（~/.seek 不存在）"
  fi
} | tee "$OUT_DIR/isolation.txt"
[ "${SERVE_ENV_HITS:-0}" -ge 1 ] || { echo "FAIL: serve 进程环境未见 SEEK_HOME 沙盒——隔离无法自证"; exit 1; }
[ "${FD_HITS:-1}" -eq 0 ] || { echo "FAIL: serve 进程树打开了真实 ~/.seek 下的文件——隔离被破坏"; exit 1; }

NPNG=$(find "$OUT_DIR" -name '*.png' | wc -l | tr -d ' ')
echo "== 产物清点：$OUT_DIR =="
ls "$OUT_DIR" | sed 's/^/  /'
[ -f "$OUT_DIR/manifest.json" ] || { echo "FAIL: manifest.json 缺"; exit 1; }
[ "$NPNG" = "32" ] || { echo "FAIL: PNG ${NPNG}/32"; exit 1; }
[ -f "$OUT_DIR/console.txt" ] || { echo "FAIL: console.txt 缺"; exit 1; }
[ -f "$OUT_DIR/dom-chat-rich.html" ] || { echo "FAIL: dom-chat-rich.html 缺"; exit 1; }
echo "审查证据包齐（32 PNG + manifest + console + DOM dump + 沙盒自证）。"
echo "人工/定时挑刺入口：逐 PNG 目检 + console.txt 过一遍异常行。"
