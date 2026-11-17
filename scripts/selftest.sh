#!/bin/bash
# selftest.sh — seek 仓验证车道自测脚本的固定入口与自动发现分发
# （c_aqbdaz，2026-11-17：登记制→发现制；前身 c_i2cyb7 2026-11-13）。
#
# 白名单形态不变（c_ck6djl B 方案，2026-11-08 用户拍板）：claude-judge
# 验证白名单按整条命令文本匹配，只放行本固定入口一条
# Bash(bash scripts/selftest.sh:*)——名单分发收进本文件（仓库受审代码，
# 照常过验证与拟人两道闸才进 main），活配置永不因新脚本再动。
#
# 为什么改发现制（出生事故两例）：c_6py7gh 与 c_zcygo2 的验证车道均因
# 「新脚本写完忘了登记」被 don't-ask mode 拒跑——未登记名不在入口映射里，
# 直接 bash scripts/<新脚本> 又不在白名单——整卡 unverified 停人审、
# 主会话手跑补证收口（#7848）。登记制把覆盖寄托在「写脚本的会话记得回来
# 改入口文件」上，这一步可以忘；发现制把忘掉做成不可能：scripts/*.sh
# 全量自动进名单，新脚本零登记自动覆盖，不适宜自测的进显式排除表。
#
# 与被否决的通配方案（c_ck6djl：Bash(bash scripts/*-test.sh) 两条，拟人
# ask_human 否决——通配=判定车道自扩权 + 与 Edit(//tmp/**) 组合可借
# scripts/../../../../tmp/ 穿越执行判定模型临场自写的 shell）的边界：
# 本设计白名单仍是唯一固定入口一条；发现只发生在本文件（受审代码）内部；
# 参数仍禁构造路径——只与 glob 派生的基名逐一比对，命中的执行路径取自
# glob 结果本身；含 / 或 .. 的参数照旧先拒。scripts/ 下的文件能出现在
# main 本身就过了两道闸——这是与 /tmp 临场自写文件被放行的本质区别。
#
# 机制：发现名单 = scripts/*.sh 基名 − 排除表；--list 全量照列（发现项
# 带目标与分发参数、排除项带理由）。裸跑形态不是自测、但自带确定性自测
# 模式的脚本，在 dispatch_args 登记固定分发参数（kernel-deps.sh 的
# --self-test，c_aqbdaz 任务②）。
#
# 进名单的口径（c_i2cyb7 规矩 1 沿用）：确定性全绿的自测/冒烟。需要
# 真实 LLM key/网络的、闸语义（超限即红是职责）的、有真实副作用的，
# 一律进排除表并逐项注理由——排除表非空且每项有理由是硬要求。
#
# 用法：bash scripts/selftest.sh <名>    分发跑单个自测脚本（透传退出码）
#       bash scripts/selftest.sh --list  列发现名单与排除表
set -u

usage() {
  echo "用法：bash scripts/selftest.sh <名|--list>（发现名单看 --list）" >&2
  exit 2
}

[ "$#" -eq 1 ] || usage
name=$1

# 排除表（发现制的另一半，本文件唯一真源）：不适宜自测的脚本 + 逐项理由。
# 新脚本默认进名单；确不适宜的在此加一行 case 臂，理由写清楚。
excluded_reason() {
  case ${1} in
    selftest)
      echo "入口自身（分发器）：裸跑只打 usage 退出 2，无自测语义" ;;
    eval-trend)
      echo "无参形态=对 eval/results 真数据出趋势报告，内容随数据演进、非确定性全绿（c_onl4et 口径）；确定性自测走发现的 eval-trend-test（wrapper 固定传 --selftest）" ;;
    install-serve)
      echo "launchd 安装器：裸跑默认 install——写 ~/Library/LaunchAgents + launchctl load，真实系统副作用，非沙盒自测材料" ;;
    verify-doc-budgets)
      echo "文档预算闸：超限即红是它的职责，进「跑全绿」入口语义打架（c_i2cyb7 既定排除口径）" ;;
    web-gate)
      echo "体积/零构建链闸：trip 即红是职责（同 verify-doc-budgets 闸语义）+ 每跑一次全量交叉编译 linux/amd64 二进制，重" ;;
    pkg-inventory)
      echo "测量报告工具：全仓 go test -cover 分钟级、且测试失败也不红（|| true 吞掉）——绿无可信度、费时不增值" ;;
    web-memory-manual|web-sessions-manual|web-stream-manual)
      echo "手测脚本：起真 serve 后打印手测清单、等人按 Enter 收尾（交互阻塞），浏览器验收是人活" ;;
    *)
      return 1 ;;
  esac
}

# 分发参数表：裸跑非自测形态、但自带确定性自测模式的脚本 → 固定分发参数
# （一个基名一条臂、一个参数；参数是本文件里的字面量，不接受外部传入）。
dispatch_args() {
  case ${1} in
    kernel-deps) echo "--self-test" ;;  # 报告模式恒 exit 0（advisory），自测在 --self-test（c_zcygo2 自带）
    *)           return 1 ;;
  esac
}

# 与白名单调用形态（bash scripts/selftest.sh …）对齐：入口以仓库根解析目标。
cd "$(dirname "$0")/.." || exit 2

if [ "${name}" = "--list" ]; then
  echo "# 发现制（c_aqbdaz）：scripts/*.sh 全量自动进名单，零登记；排除表逐项注理由"
  for f in scripts/*.sh; do
    n=$(basename "${f}" .sh)
    if reason=$(excluded_reason "${n}"); then
      printf '%-24s [排除] %s\n' "${n}" "${reason}"
    elif a=$(dispatch_args "${n}"); then
      printf '%-24s %s （分发参数：%s）\n' "${n}" "${f}" "${a}"
    else
      printf '%-24s %s\n' "${n}" "${f}"
    fi
  done
  exit 0
fi

case ${name} in
  */*|*..*)
    echo "selftest: 拒绝路径形态参数（含 / 或 ..）：${name}——入口只认发现名单基名，--list 看名单" >&2
    exit 2
    ;;
esac

# 发现即真源：目标路径取自 glob 结果，参数只用于与基名比对（禁构造路径）。
target=""
for f in scripts/*.sh; do
  if [ "$(basename "${f}" .sh)" = "${name}" ]; then
    target=${f}
    break
  fi
done

if [ -z "${target}" ]; then
  echo "selftest: 未发现的名：${name}（bash scripts/selftest.sh --list 看名单；新脚本放 scripts/ 下即自动进）" >&2
  exit 2
fi

if reason=$(excluded_reason "${name}"); then
  echo "selftest: ${name} 在排除表，不分发：${reason}" >&2
  exit 2
fi

if a=$(dispatch_args "${name}"); then
  exec bash "${target}" "${a}"
fi
exec bash "${target}"
