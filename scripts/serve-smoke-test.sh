#!/usr/bin/env bash
#
# serve-smoke-test.sh — web 控制平面（seek serve --frontend=web）的
# HTTP 契约冒烟，卡 c_snerqc（2026-11-15 安全面返工版）。三断言在
# Go 测试 TestServePlane_WebSmoke（internal/servecli/websmoke_test.go）
# 内实跑——真 servePlane 组装（= `seek serve` 的执行体）+ 真监听 +
# 真 M2 路由，全链 HTTP→Inbound→router→Send→SSE 无 fake：
#
#   断言① GET  /       无 token → 200 且是 HTML 页（页面无秘密）
#   断言② POST /msg    无 token → 401（token 门在一切之前）
#   断言③ SSE  /events 带token订阅 + POST /msg（带token）→ 未绑定会话
#                      由 M2 路由答 unboundHint 回 SSE；收尾断言收干
#                      契约：ctx cancel（SIGTERM 替身）→ 排空 → 退出 0
#
# 监听口（web_addr）由测试内 WebConfig.Addr 注入随机 loopback 口：
# 生产代码没有也不需要 SEEK_WEB_ADDR 这类不限网段的环境出口
# （安全面拍板 2026-11-15——监听地址属暴露面，对外可配走 c_x7ehom
# 的限网段通道，loopback+tailnet 之外启动即拒，不在本卡）。
# 沙盒隔离由测试内 t.Setenv(SEEK_HOME)+t.TempDir 承担，不碰真实
# ~/.seek 与生产 8790。
#
# Env:
#   DRY_RUN=1   只打印将执行的步骤，不编译不跑测试（退出 0）
#
# 退出码：0 三断言全绿；1 测试失败或编译失败；2 用法错误。
# 依赖：go（不再依赖 curl —— SSE 消费内嵌在测试里）。
set -u

usage() {
  echo "用法：bash scripts/serve-smoke-test.sh（不接受参数；env 见文件头）" >&2
  exit 2
}
[ "$#" -eq 0 ] || usage

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "${HERE}")"

# ---- DRY_RUN：只打印不动手（butler 自测范式） ----
if [ "${DRY_RUN:-0}" = "1" ]; then
  echo "DRY_RUN: go test -count=1 -race -run '^TestServePlane_WebSmoke\$' ./internal/servecli"
  echo "DRY_RUN:   断言① GET / → 200+HTML；断言② POST /msg 无 token → 401；断言③ SSE 收 unboundHint；收干 → 退出 0"
  exit 0
fi

cd "${REPO}" || exit 1
# exec：退出码原样透传 go test（0 绿 / 1 红），不留 shell 残影。
exec go test -count=1 -race -run '^TestServePlane_WebSmoke$' ./internal/servecli
