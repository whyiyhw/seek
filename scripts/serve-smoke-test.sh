#!/usr/bin/env bash
#
# serve-smoke-test.sh — web 控制平面（seek serve --frontend=web）的
# HTTP 契约冒烟，卡 c_snerqc。把 web MVP 的手工冒烟固化成 butler
# 自测脚本范式（假 SEEK_HOME 沙盒 + 真退出码 + DRY_RUN 预演）：
#
#   断言① GET  /       无 token → 200 且是 HTML 页（页面无秘密）
#   断言② POST /msg    无 token → 401（token 门在方法/解析之前）
#   断言③ SSE  /events 带token订阅后，POST /msg（带token）注入的上行
#                      经 M2 路由回到 SSE——未绑定会话收到 unboundHint，
#                      全链路 HTTP→Inbound→router→Send→SSE 不需要真
#                      executor（无绑定即无 run，卡面「fake executor」项
#                      天然满足）
#
# 隔离：SEEK_HOME 指向 mktemp 假家（web.token / bindings.json 全落
#       沙盒）；监听口随机高位（SEEK_WEB_ADDR，首口被占自动换口重试），
#       不碰生产 8790 与真实 ~/.seek；二进制与日志也全在沙盒，EXIT 即清。
#       收尾顺带断言 plane 的退出码契约：SIGTERM → 排空 → 退出 0。
#
# Env:
#   DRY_RUN=1          只打印将执行的步骤，不构建不起 serve（退出 0）
#   SERVE_SMOKE_PORT   钉死首次尝试端口——端口冲突兜底的测试缝：该口
#                      被占时脚本应换随机口重试并最终全绿
#   SEEK_SMOKE_BIN     复用已构建的 seek 二进制（缺省在沙盒内 go build）
#
# 退出码：0 全绿；1 任一断言/启动失败；2 用法错误。
# 依赖：curl、go（缺省路径时）。Bash 3.2 兼容（无关联数组/mapfile）。
set -u

usage() {
  echo "用法：bash scripts/serve-smoke-test.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"

fail() { echo "✗ $1"; exit 1; }
step() { echo "== $1"; }

# ---- DRY_RUN：只打印不动手（butler 自测范式） ----
if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: 构建 ${SEEK_SMOKE_BIN:-<沙盒内 go build ./cmd/seek>}"
  echo "DRY_RUN: 以 SEEK_HOME=<mktemp 假家> SEEK_WEB_ADDR=127.0.0.1:${SERVE_SMOKE_PORT:-<随机高位口>} 起 seek serve --frontend=web（冲突换口，≤5 次）"
  echo "DRY_RUN: 断言① GET / → 200+HTML；断言② POST /msg 无 token → 401；断言③ SSE 收注入回复（unboundHint）"
  echo "DRY_RUN: SIGTERM 收干（断言 serve 退出码 0）并清理沙盒"
  exit 0
fi

command -v curl >/dev/null 2>&1 || fail "需要 curl（SSE 与三断言都靠它）"

SANDBOX="$(mktemp -d /tmp/seek-serve-smoke-XXXXXX)"
FAKE_HOME="${SANDBOX}/home"
SERVE_LOG="${SANDBOX}/serve.log"
SSE_LOG="${SANDBOX}/sse.log"
PAGE_FILE="${SANDBOX}/page.html"
MSG_FILE="${SANDBOX}/msg-resp.json"
SERVE_PID=""
SSE_PID=""
cleanup() {
  # kill 后紧跟 wait：既收尸，也免 bash 在 trap 里替被杀的后台任务
  # 打「Terminated: 15」噪音
  if [ -n "${SSE_PID}" ]; then
    kill "${SSE_PID}" 2>/dev/null
    wait "${SSE_PID}" 2>/dev/null
  fi
  if [ -n "${SERVE_PID}" ]; then
    kill "${SERVE_PID}" 2>/dev/null
    wait "${SERVE_PID}" 2>/dev/null
  fi
  rm -rf "${SANDBOX}"
}
trap cleanup EXIT

# ---- 构建（本仓源码的真二进制；可注入现成的） ----
step "构建被测二进制"
if [ -n "${SEEK_SMOKE_BIN:-}" ]; then
  BIN="${SEEK_SMOKE_BIN}"
  [ -x "${BIN}" ] || fail "SEEK_SMOKE_BIN 不可执行：${BIN}"
else
  BIN="${SANDBOX}/seek"
  (cd "${REPO}" && go build -o "${BIN}" ./cmd/seek) || fail "go build 失败"
fi

# ---- 起临时 serve：随机高位口，冲突兜底换口重试 ----
# 就绪判据是 banner「web: up: http://127.0.0.1:<port>/」——listen 成功
# 才写；口被占时唯一 frontend 的 Run 立刻报错、整个 plane 快速退出
# （exit 2），两种结局都轮询得到。
pick_port() { echo $(( (RANDOM % 41000) + 20000 )); }

start_serve_on() { # <port>；0=起活（SERVE_PID 就绪），1=该口失败
  local port="$1" i
  : > "${SERVE_LOG}"
  SEEK_HOME="${FAKE_HOME}" SEEK_WEB_ADDR="127.0.0.1:${port}" \
    "${BIN}" serve --frontend=web >"${SERVE_LOG}" 2>&1 &
  SERVE_PID=$!
  for i in $(seq 1 80); do # 8s 上限
    if grep -q "web: up: http://127.0.0.1:${port}/" "${SERVE_LOG}" 2>/dev/null; then
      return 0
    fi
    if ! kill -0 "${SERVE_PID}" 2>/dev/null; then
      wait "${SERVE_PID}" 2>/dev/null
      SERVE_PID=""
      return 1
    fi
    sleep 0.1
  done
  kill "${SERVE_PID}" 2>/dev/null
  wait "${SERVE_PID}" 2>/dev/null
  SERVE_PID=""
  return 1
}

step "起 serve（随机高位口 + 假 SEEK_HOME，冲突自动换口）"
mkdir -p "${FAKE_HOME}"
PORT=""
attempt=0
try="${SERVE_SMOKE_PORT:-$(pick_port)}"
while [ "${attempt}" -lt 5 ]; do
  attempt=$((attempt + 1))
  if start_serve_on "${try}"; then
    PORT="${try}"
    break
  fi
  echo "   端口 ${try} 起不来（被占或启动失败），换口重试（${attempt}/5）"
  try="$(pick_port)"
done
if [ -z "${PORT}" ]; then
  echo "--- serve.log 末 20 行 ---"
  tail -20 "${SERVE_LOG}" 2>/dev/null || true
  fail "5 次尝试均未起活 serve"
fi
BASE="http://127.0.0.1:${PORT}"
echo "   serve 已起活：${BASE}（SEEK_HOME=${FAKE_HOME}）"

# ---- 断言①：GET / 无 token → 200 + HTML ----
step "断言① GET / → 200 且为 HTML"
code="$(curl -s -o "${PAGE_FILE}" -w '%{http_code}' "${BASE}/")"
[ "${code}" = "200" ] || fail "GET / 状态码 ${code}，想要 200"
grep -qi '<!doctype html' "${PAGE_FILE}" \
  || fail "GET / 响应不是 HTML 页（前 80 字节：$(head -c 80 "${PAGE_FILE}")）"

# ---- 断言②：POST /msg 无 token → 401 ----
step "断言② POST /msg 无 token → 401"
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST \
  -H 'Content-Type: application/json' -d '{"text":"hi"}' "${BASE}/msg")"
[ "${code}" = "401" ] || fail "POST /msg 无 token 状态码 ${code}，想要 401"

# ---- 断言③：SSE 收注入回复 ----
step "断言③ SSE 收注入回复（POST /msg → router → SSE）"
TOKEN="$(cat "${FAKE_HOME}/serve/web.token" 2>/dev/null)" \
  || fail "web.token 不存在（banner 后应已生成）"
[ "${#TOKEN}" -ge 16 ] || fail "web.token 长度异常：${#TOKEN} 字节"

curl -sN "${BASE}/events?token=${TOKEN}" >"${SSE_LOG}" 2>&1 &
SSE_PID=$!
# 等订阅挂上：handleEvents 先写 retry 行、再注册订阅者；见到 retry 行
# 后再留 0.5s 余量，覆盖注册的微秒级窗口。
for i in $(seq 1 50); do
  grep -q '^retry: 3000' "${SSE_LOG}" 2>/dev/null && break
  kill -0 "${SSE_PID}" 2>/dev/null \
    || fail "SSE 连接提前断开：$(tail -c 200 "${SSE_LOG}")"
  sleep 0.1
done
grep -q '^retry: 3000' "${SSE_LOG}" || fail "SSE 5s 内未握手（无 retry 行）"
sleep 0.5

code="$(curl -s -o "${MSG_FILE}" -w '%{http_code}' -X POST \
  -H 'Content-Type: application/json' -d '{"text":"冒烟 ping"}' \
  "${BASE}/msg?token=${TOKEN}")"
[ "${code}" = "202" ] \
  || fail "POST /msg（带 token）状态码 ${code}，想要 202（回复：$(cat "${MSG_FILE}")）"

# 未绑定会话的回复是 unboundHint（router.go），10s 内必须落到 SSE。
for i in $(seq 1 100); do
  if grep -q '"conv":"web:default"' "${SSE_LOG}" 2>/dev/null \
    && grep -q '还没绑定' "${SSE_LOG}"; then
    break
  fi
  sleep 0.1
done
grep -q '"conv":"web:default"' "${SSE_LOG}" \
  || fail "SSE 未收到 web:default 事件（10s）：$(tail -c 300 "${SSE_LOG}")"
grep -q '还没绑定' "${SSE_LOG}" \
  || fail "SSE 事件不是未绑定提示 unboundHint：$(tail -c 300 "${SSE_LOG}")"

# ---- 收干：SIGTERM → 排空 → 退出码 0（plane 的退出码契约） ----
step "SIGTERM 收干：serve 排空退出 0"
kill "${SSE_PID}" 2>/dev/null # 先撤 SSE 订阅（模拟关页签）
wait "${SSE_PID}" 2>/dev/null
SSE_PID=""
kill -TERM "${SERVE_PID}"
# 直接 wait：排空预算由 Serve 兜底（DrainWait+2×HandlerGrace），不会永久卡
wait "${SERVE_PID}"
src=$?
SERVE_PID=""
[ "${src}" -eq 0 ] || fail "serve 退出码 ${src}，想要 0（干净排空）"

echo "✓ serve web 冒烟三断言全绿（端口 ${PORT}，SEEK_HOME=${FAKE_HOME}）"
