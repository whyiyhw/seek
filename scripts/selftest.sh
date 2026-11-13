#!/bin/bash
# selftest.sh — seek 仓验证车道自测脚本的固定入口与白名单分发
# （c_i2cyb7，2026-11-13；与 butler 仓 scripts/selftest.sh 同款协议）。
#
# 背景：claude-judge 验证白名单按整条命令文本匹配（~/.paseo/config.json
# agents.providers.claude-judge.options.allowedTools），逐条枚举各仓脚本
# 跟不上分支——seek 仓此前无注册入口，ndJson 冒烟（c_bnjrw5 第 7 项）、
# weight.sh（c_vt7fiw）只能主会话手跑补证，「验证未跑成」反复堵卡。
# 跨仓统一：白名单已有的 Bash(bash scripts/selftest.sh:*) 一条即覆盖本
# 入口（命令文本匹配、不分仓）——名单分发收进本文件，仓库受审代码照常
# 过验证与拟人两道闸才进 main。
#
# 机制（与 butler 卡面钉死的口径逐字一致）：参数逐字 case 匹配映射到
# 固定脚本路径（禁 eval、禁由参数拼路径）；拒绝未登记名与含 / 或 .. 的
# 参数；透传退出码；--list 照列全部登记名——目标文件暂不存在的也照列
# （跑时报明确错误非零），登记卡与本入口卡的合并顺序解耦。
#
# 上架规矩：新增验收自测脚本 = 加 scripts/<名>-test.sh 文件 + 在
# target_of 的 case 与 ALL_NAMES 各登记一处；白名单规则形态不变，活
# 配置无需再动。两条登记纪律（c_6py7gh 同款）：
#   1. 只登记确定性全绿的验收自测——需要真实 LLM key/网络的（eval 全量、
#      real-API 冒烟）不登记，车道判 green 必须可复现；
#      scripts/verify-doc-budgets.sh 有意不登记：超限即红是它的职责，
#      注册进「跑全绿」入口会让语义打架（AGENTS.md 词数闸另有 CI 形态）。
#   2. selftest 本身即注册入口——「selftest 可跑」的验收 = 真跑各登记
#      名全绿（c_i2cyb7 验收口径）。
#
# 用法：bash scripts/selftest.sh <名>    分发跑单个自测脚本（透传退出码）
#       bash scripts/selftest.sh --list  列全部登记名与目标文件
set -u

usage() {
  echo "用法：bash scripts/selftest.sh <名|--list>（登记名单看 --list）" >&2
  exit 2
}

[ "$#" -eq 1 ] || usage
name=$1

# 白名单映射（本文件唯一真源）：登记名 → 固定脚本路径。每条 case 臂映射
# 整条字面路径，未匹配即拒——参数只用来选臂，不参与构造路径。
target_of() {
  case ${1} in
    acp-smoke) echo "scripts/acp-smoke-test.sh" ;;
    weight)    echo "scripts/weight.sh" ;;
    *)         return 1 ;;
  esac
}

# 登记名清单（--list 与映射自检共用；与 target_of 的 case 臂同步维护——
# 清单有而 case 无臂会被 --list 的自检抓出来，非零退出）。
ALL_NAMES="acp-smoke weight"

# 与白名单调用形态（bash scripts/selftest.sh …）对齐：入口以仓库根解析目标。
cd "$(dirname "$0")/.." || exit 2

if [ "${name}" = "--list" ]; then
  rc=0
  for n in ${ALL_NAMES}; do
    if t=$(target_of "${n}"); then
      if [ -f "${t}" ]; then
        printf '%-24s %s\n' "${n}" "${t}"
      else
        printf '%-24s %s  （目标文件暂不存在：登记卡未合并或仓库不完整）\n' "${n}" "${t}"
      fi
    else
      echo "selftest 内部错误：ALL_NAMES 有 ${n}、target_of 无映射（两处需同步改）" >&2
      rc=3
    fi
  done
  exit ${rc}
fi

case ${name} in
  */*|*..*)
    echo "selftest: 拒绝路径形态参数（含 / 或 ..）：${name}——入口只认登记名，--list 看名单" >&2
    exit 2
    ;;
esac

if ! target=$(target_of "${name}"); then
  echo "selftest: 未登记的名：${name}（bash scripts/selftest.sh --list 看名单）" >&2
  exit 2
fi

if [ ! -f "${target}" ]; then
  echo "selftest: 已登记 ${name}，但目标文件 ${target} 暂不存在（登记卡未合并或仓库不完整），无法分发" >&2
  exit 1
fi

exec bash "${target}"
