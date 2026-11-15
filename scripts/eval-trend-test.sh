#!/bin/bash
# eval-trend-test.sh — eval-trend.sh 的 selftest 登记位（卡 c_onl4et，2026-11-14）
#
# 为什么是 wrapper 而不是把 scripts/eval-trend.sh 直接登进 target_of：
# selftest 入口对目标是无参分发（exec bash <target>），而 eval-trend.sh 的
# 无参形态=对 eval/results 真实数据出趋势报告——内容随 results 演进、首份
# 报告即含 2 failing 例（c_lxferp d57ae2e），不是确定性全绿；确定性自测在
# 它的 --selftest 模式（假 results 目录驱动全旗标矩阵，TREND_NOW 钉死）。
# 按 selftest.sh 头注释上架规矩（scripts/<名>-test.sh + 两处登记）与
# c_6py7gh 规矩 1「只登记确定性全绿的验收自测」，拆本 wrapper 固定传
# --selftest。
#
# 合并顺序解耦（selftest.sh 头注释同款口径）：eval-trend.sh 本体在 c_lxferp
# 卡分支（drive/c_lxferp，review 中）——本体未进 main 前本 wrapper 报文件
# 不存在非零退出，属登记入口设计内行为；两卡任一先合并都不破坏入口。
exec bash "$(dirname "$0")/eval-trend.sh" --selftest
