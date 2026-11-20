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
# 本脚本把该形态收进脚本内部：对 web-stream-manual.sh 与 web-md-manual.sh
# 各跑一次完整脚本（CHROME 指向不存在路径、stdin 接 /dev/null），断言
#   1. rc=3（无人值守车道报环境能力缺，不假装绿）；
#   2. stderr 含「FAIL: 无头验收条件缺（Chrome」且点名 CHROME 给的路径
#      （证明 miss 计算走的是 env 而非默认位）；
#   3. 现场清理干净——两个 manual 脚本各自 trap cleanup EXIT 的可观测
#      效果：/tmp 下对应前缀无**新增**残留目录、serve 端口无监听残留。
# 全程不碰真实 ~/.seek 与生产 8790（子脚本自带临时 SEEK_HOME 与独立端口）。
#
# Env: 无（子脚本 ADDR 用各自默认 18791/18795；跑前查端口空闲，被占即
#      FAIL 并跳过该臂——避免把别人的现场误判成清理失败）。
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
  exit 0
fi

fail=0
ck() { # ck <名字> <期望> <实际>
  if [ "$2" = "$3" ]; then echo "  ok  $1 = $3"
  else echo "  FAIL $1：want [$2] got [$3]"; fail=1; fi
}
has() { printf '%s' "$1" | grep -qF "$2" && echo yes || echo no; }

port_listening() { # port_listening <端口> → yes/no（lsof 无匹配退 1，正常）
  [ -n "$(lsof -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null)" ] && echo yes || echo no
}

new_leftover() { # new_leftover <glob> <跑前快照>：打印跑后新增路径（空=无残留）
  local post
  post=$(ls -d $1 2>/dev/null || true)   # glob 刻意不引用：要的是展开
  comm -13 <(printf '%s\n' "$2" | sort) <(printf '%s\n' "$post" | sort)
}

# check_one <基名> <端口> <tmp 前缀 glob>：一臂三断言 + 清理两查。
check_one() {
  local base=$1 port=$2 prefix_glob=$3
  local script="$HERE/${base}.sh"
  echo "== ${base}：CHROME=/nonexistent/nope + stdin </dev/null =="
  if [ "$(port_listening "$port")" = "yes" ]; then
    echo "  FAIL ${base}：端口 ${port} 跑前已被占——先把占者清走再自测（避免误判清理）"
    fail=1
    return
  fi
  local pre out err rc errtext
  pre=$(ls -d ${prefix_glob} 2>/dev/null || true)
  out=$(mktemp) || { echo "  FAIL ${base}：mktemp 失败" >&2; fail=1; return; }
  err=$(mktemp) || { echo "  FAIL ${base}：mktemp 失败" >&2; rm -f "$out"; fail=1; return; }
  # env 前缀在脚本内部：车道白名单只见 bash scripts/selftest.sh <本名>。
  CHROME=/nonexistent/nope bash "$script" </dev/null >"$out" 2>"$err"
  rc=$?
  errtext=$(cat "$err")
  rm -f "$out" "$err"
  ck "${base}：rc（无人值守报能力缺）" "3" "${rc}"
  ck "${base}：stderr 含无头条件缺文案" "yes" "$(has "$errtext" 'FAIL: 无头验收条件缺（Chrome')"
  ck "${base}：stderr 点名 CHROME 给的缺失路径" "yes" "$(has "$errtext" '/nonexistent/nope')"
  ck "${base}：/tmp 无新增残留（${prefix_glob}）" "" "$(new_leftover "${prefix_glob}" "$pre")"
  ck "${base}：跑后端口 ${port} 无监听残留" "no" "$(port_listening "$port")"
}

check_one web-stream-manual 18791 '/tmp/seek-w21-manual.*'
check_one web-md-manual    18795 '/tmp/seek-w5md-manual.*'

if [ "$fail" = 0 ]; then
  echo "非 TTY 退路自测全绿（两脚本 rc=3 + 点名 Chrome 路径 + 现场清理）。"
  exit 0
fi
echo "有 FAIL——见上" >&2
exit 1
