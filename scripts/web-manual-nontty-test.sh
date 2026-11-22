#!/usr/bin/env bash
#
# web-manual-nontty-test.sh — 手测脚本「无头条件缺 + 非 TTY → exit 3」退路
# 的确定性自测（c_gqo32q 返工轮，2026-11-20）。
#
# 为什么需要它：验证车道白名单按**整条命令文本**匹配，只放行
# bash scripts/selftest.sh:* 一条——`CHROME=/nonexistent/nope bash
# scripts/web-md-manual.sh` 这种 env 前缀形态必被 don't-ask mode 拒。
# 于是「CHROME 指向不存在路径 + stdin </dev/null 时 rc=3」这半条验收在
# 车道里没有可表达的入口，上一轮提交信息里的「沙盒实跑 rc=3」无从复核。
# 本脚本把该形态收进脚本内部：对 web-stream-manual.sh / web-md-manual.sh /
# web-mobile-manual.sh（c_lqtt2m 起新增的第三条）各跑一次完整脚本
# （CHROME 指向不存在路径、stdin 接 /dev/null），断言
#   1. rc=3（无人值守车道报环境能力缺，不假装绿）；
#   2. stderr 含「FAIL: 无头验收条件缺（Chrome」且点名 CHROME 给的路径
#      （证明 miss 计算走的是 env 而非默认位）；
#   3. 现场清理干净——各 manual 脚本自己 trap cleanup EXIT 的可观测
#      效果：/tmp 下对应前缀无**新增**残留目录、serve 端口无监听残留。
# 全程不碰真实 ~/.seek 与生产 8790（子脚本自带临时 SEEK_HOME 与独立端口）。
#
# Env: 无。子脚本的 ADDR 由本脚本注入（三条区间基准 18820 / 18900 / 18980，
#      各加 $$ 散列偏移后在 16 宽内取第一个空闲口）——同机并行跑多份本自测
#      不会互相误判成「清理失败」；落点全在 18820–19059，避开既有手测脚本的
#      18790–18797/18811 与生产 8790。
#
# 退出码：0 全部断言过；1 断言失败或环境冲突；2 用法错误。
# 依赖：go、curl（经子脚本）；本脚本自身不依赖 Chrome/node——被测的
#       正是「Chrome 缺」分支。
set -u

usage() {
  echo "用法：bash scripts/web-manual-nontty-test.sh（不接受参数）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"

if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: CHROME=/nonexistent/nope bash scripts/web-stream-manual.sh </dev/null → rc=3 + stderr 点名 + 清理"
  echo "DRY_RUN: CHROME=/nonexistent/nope bash scripts/web-md-manual.sh    </dev/null → rc=3 + stderr 点名 + 清理"
  echo "DRY_RUN: CHROME=/nonexistent/nope bash scripts/web-mobile-manual.sh </dev/null → rc=3 + stderr 点名 + 清理"
  exit 0
fi

fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want [$2] got [$3]"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF "$2" && echo yes || echo no; }

# port_listening <端口> → yes/no。优先 lsof（只认 LISTEN）；环境里没有 lsof 时
# 退 bash /dev/tcp 探测——但绝不静默返回 no：那会把「端口无监听残留」两条断言
# 变成空断言（本卡整条纪律针对的就是这种无凭据的绿）。两条路径都实探。
port_listening() {
  if command -v lsof >/dev/null 2>&1; then
    [ -n "$(lsof -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null)" ] && echo yes || echo no
    return
  fi
  if (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; then echo yes; else echo no; fi
}

new_leftover() { # new_leftover <glob> <跑前快照>：打印跑后新增路径（空=无残留）
  local post
  post=$(ls -d $1 2>/dev/null || true)   # glob 刻意不引用：要的是展开
  comm -13 <(printf '%s\n' "$2" | sort) <(printf '%s\n' "$post" | sort)
}

# free_port <起点> <跨度>：在 [起点, 起点+跨度) 里返回第一个无监听的端口，找不到退 1。
free_port() {
  local p=$1 end=$(( $1 + $2 ))
  while [ "$p" -lt "$end" ]; do
    if [ "$(port_listening "$p")" = "no" ]; then echo "$p"; return 0; fi
    p=$(( p + 1 ))
  done
  return 1
}

# check_one <基名> <端口起点> <端口跨度> <tmp 前缀 glob>：一臂三断言 + 清理两查。
# 端口不写死：从起点往上找一个无监听的（ADDR 透传给子脚本），跑完再查它是否
# 还留着监听。这样同机并行跑多份自测不会互相误判成「清理失败」。注意这里没有
# 「端口被占就跳过该臂」的分支——跳过＝那两条清理断言变成不存在的断言，
# 与本卡整条纪律（不许无凭据的绿）相悖；腾不出端口就让它红。
check_one() {
  local base=$1 hint=$2 span=$3 prefix_glob=$4
  local script="$HERE/${base}.sh"
  echo "== ${base}：CHROME=/nonexistent/nope + stdin </dev/null =="
  local port
  if ! port=$(free_port "$hint" "$span"); then
    echo "  FAIL ${base}：${hint}–$(( hint + span - 1 )) 全被占——腾一个端口再自测"
    fail=1
    return
  fi
  local pre out err rc errtext
  pre=$(ls -d ${prefix_glob} 2>/dev/null || true)
  out=$(mktemp) || { echo "  FAIL ${base}：mktemp 失败" >&2; fail=1; return; }
  err=$(mktemp) || { echo "  FAIL ${base}：mktemp 失败" >&2; fail=1; rm -f "$out"; return; }
  # env 前缀在脚本内部：车道白名单只见 bash scripts/selftest.sh <本名>。
  ADDR="127.0.0.1:${port}" CHROME=/nonexistent/nope bash "$script" </dev/null >"$out" 2>"$err"
  rc=$?
  errtext=$(cat "$err")
  rm -f "$out" "$err"
  ck "${base}：rc（无人值守报能力缺）" "3" "${rc}"
  ck "${base}：stderr 含无头条件缺文案" "yes" "$(has "$errtext" 'FAIL: 无头验收条件缺（Chrome')"
  ck "${base}：stderr 点名 CHROME 给的缺失路径" "yes" "$(has "$errtext" '/nonexistent/nope')"
  ck "${base}：/tmp 无新增残留（${prefix_glob}）" "" "$(new_leftover "${prefix_glob}" "$pre")"
  ck "${base}：跑后端口 ${port} 无监听残留" "no" "$(port_listening "$port")"
}

# 端口落点：三条区间各自 [基准, 基准+64) 取第一个空闲口、跨度 16，
#   基准 = 18820 / 18900 / 18980 + $$ % 64 —— 全部避开既有手测脚本占用的
#   18790–18797 与新脚本自身的 18811，三区间相隔 80 宽永不交叠，也远离生产 8790。
check_one web-stream-manual $(( 18820 + $$ % 64 )) 16 '/tmp/seek-w21-manual.*'
check_one web-md-manual    $(( 18900 + $$ % 64 )) 16 '/tmp/seek-w5md-manual.*'
check_one web-mobile-manual $(( 18980 + $$ % 64 )) 16 '/tmp/seek-w5mobile-manual.*'

if [ "$fail" = 0 ]; then
  echo "非 TTY 退路自测全绿（三脚本 rc=3 + 点名 Chrome 路径 + 现场清理）。"
  exit 0
fi
echo "有 FAIL——见上" >&2
exit 1
