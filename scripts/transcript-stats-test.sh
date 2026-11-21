#!/bin/bash
# transcript-stats-test.sh — scripts/transcript-stats 的确定性自测入口
# （c_254zzz；裸跑即自测，自动进 selftest.sh 发现名单，无 dispatch 参数）。
#
# 为什么存在：分析器本体是 scripts/transcript-stats（无 .sh 后缀，符合卡面
# 约定的调用名），而 selftest.sh 的发现制只覆盖 scripts/*.sh——这个瘦
# wrapper 把 `--selftest` 挂进发现名单，让 fixture 断言进「跑全绿」入口。
#
# 自测只读 eval/fixtures/transcript-stats/ 下的合成 transcript 并写 mktemp
# 目录，不碰 ~/.seek 真实数据、不联网、不需 key —— 满足进名单的口径。
set -u
cd "$(dirname "$0")/.." || exit 2

command -v python3 >/dev/null 2>&1 || { echo "transcript-stats-test: python3 required" >&2; exit 2; }

exec python3 scripts/transcript-stats --selftest
