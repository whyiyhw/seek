#!/usr/bin/env bash
#
# web-w6-final.sh — W6-9 c_3dz3kg 终验对照包的一条命令入口：按卡面 ①②③ 跑
#   OUT_DIR=out/w6-final W6_FINAL=1 bash scripts/web-page-review.sh
# （32 张 + compare.html + probe.md），再核三件产物在不在。
# 产物全部在 out/（gitignored）：不进 embed、不入库；compare.html 用同目录相对
# 路径引图，dsh 参考图走 ../../docs/notes-assets/——挪出仓库就看不到右栏。
# 只记录不拦外观：退出码只看「产物齐不齐」；数值是否一致读 probe.md。
#
# Env: CHROME=<无头浏览器路径>（透传）；DRY_RUN=1 只打印步骤。
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "$HERE")"
if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: OUT_DIR=out/w6-final W6_FINAL=1 bash scripts/web-page-review.sh"
  echo "DRY_RUN: 核 out/w6-final/{compare.html,probe.md} + 32 张 PNG + manifest.w6_final.ok"
  exit 0
fi
cd "$REPO"
OUT_DIR=out/w6-final W6_FINAL=1 bash scripts/web-page-review.sh
fail=0
for f in compare.html probe.md; do
  [ -s "out/w6-final/$f" ] || { echo "FAIL: out/w6-final/$f 缺失或为空"; fail=1; }
done
n=$(ls out/w6-final/*.png 2>/dev/null | wc -l | tr -d ' ')
[ "$n" = "32" ] || { echo "FAIL: PNG $n/32"; fail=1; }
node -e "const m=require('./out/w6-final/manifest.json');process.exit(m.w6_final&&m.w6_final.ok?0:1)" \
  || { echo "FAIL: manifest.w6_final.ok 不为 true（对照包采集失败）"; fail=1; }
[ "$fail" = "0" ] && echo "W6 终验对照包齐：out/w6-final（32 PNG + compare.html + probe.md + manifest）"
exit "$fail"
