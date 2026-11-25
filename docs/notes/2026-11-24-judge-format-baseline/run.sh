#!/bin/bash
# 服从率实验：只测「模型吐合法 JSON 的比例」，不测判得对不对。
# 因此样本是合成的 —— 内容无关紧要，形状才是被测量对象。
#
# 复现：证据目录自带 samples/ 与 rubric.md，本脚本从自身所在目录解析路径，
# 不再依赖原始落点 /tmp/winnow-exp（那里早被清掉）。SEEK_HOME 仍指向外部沙盒
# ——实验用的假 HOME 含凭证，按纪律不入库（见同目录 ../2026-11-24-judge-format-baseline.md
# 开头那条）。
set -u
EXP=$(cd "$(dirname "$0")" && pwd)
RUBRIC=$(cat "$EXP/rubric.md")
MODEL_FLAGS=${MODEL_FLAGS:-""}

if [ -z "${SEEK_HOME_OVERRIDE:-}" ]; then
  echo "run.sh: 需要 SEEK_HOME_OVERRIDE=<含凭证的假 HOME>（本轮实验的沙盒未入库）" >&2
  exit 2
fi
export SEEK_HOME="$SEEK_HOME_OVERRIDE"

N=${N:-10}
for f in "$EXP"/samples/*.txt; do
  sample=$(basename "$f" .txt)
  for i in $(seq 1 "$N"); do
    out="$EXP/runs/${sample}-${i}.json"
    raw=$(seek -p "$RUBRIC

条目：
$(cat "$f")" \
      --json --no-save --no-project-md --tools read --max-turns 1 $MODEL_FLAGS 2>/dev/null \
      | jq -r 'select(.type=="text_delta") | .delta' 2>/dev/null | tr -d '\n')
    printf '%s\n' "$raw" > "$out"
  done
done
echo "done: $(ls "$EXP"/runs/*.json 2>/dev/null | wc -l) runs"
