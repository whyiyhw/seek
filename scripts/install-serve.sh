#!/usr/bin/env bash
# install-serve.sh — seek serve 常驻化安装/卸载（macOS launchd 用户 agent）
#
# 用法：
#   scripts/install-serve.sh install [--dry-run]    渲染模板并加载（默认动作）
#   scripts/install-serve.sh uninstall [--dry-run]  卸载：unload + 删 plist（日志保留）
#   scripts/install-serve.sh status                 查看加载状态与最近退出码
#
# 可用环境变量覆盖默认值：
#   SEEK_BIN    seek 二进制绝对路径（默认 ~/.local/bin/seek）
#   SEEK_LOG    日志文件绝对路径（默认 ~/Library/Logs/seek-serve.log）
#
# --dry-run 只打印将执行的动作和渲染结果，不写 ~/Library/LaunchAgents、
# 不 launchctl —— 合并前的沙盒验证用。
#
# 模板在 deploy/seek-serve.plist（{{SEEK_BIN}}/{{SEEK_LOG}} 占位符）。
# launchd 不展开 ~，占位符必须渲染成绝对路径，所以别手工 cp 模板。
set -euo pipefail

LABEL="com.whyiyhw.seek-serve"
LAUNCHAGENTS_DIR="$HOME/Library/LaunchAgents"
PLIST_DST="$LAUNCHAGENTS_DIR/$LABEL.plist"
REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
TMPL="$REPO_ROOT/deploy/seek-serve.plist"

SEEK_BIN="${SEEK_BIN:-$HOME/.local/bin/seek}"
SEEK_LOG="${SEEK_LOG:-$HOME/Library/Logs/seek-serve.log}"

DRY_RUN=0

die() { echo "install-serve: $*" >&2; exit 1; }
info() { echo "==> $*"; }
run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] $*"
  else
    "$@"
  fi
}

usage() {
  sed -n '2,17p' "$0" | sed -e 's/^# \{0,1\}//'
  exit 1
}

# sed_repl ESCAPE -- 为 sed 的 s@@@ 替换端转义 & 与 \（路径里出现时避免被解释）
sed_repl() { printf '%s' "$1" | sed -e 's/[&\\]/\\&/g'; }

render_template() {
  # 输出渲染后的 plist 到 stdout
  local bin log
  bin=$(sed_repl "$SEEK_BIN")
  log=$(sed_repl "$SEEK_LOG")
  sed -e "s@{{SEEK_BIN}}@$bin@g" -e "s@{{SEEK_LOG}}@$log@g" "$TMPL"
}

do_install() {
  [ -f "$TMPL" ] || die "模板不存在: $TMPL"
  if [ ! -x "$SEEK_BIN" ]; then
    die "seek 二进制不存在或不可执行: ${SEEK_BIN}
  先构建并就位（如 go install ./cmd/seek 或 cp 到 ${SEEK_BIN}），或用 SEEK_BIN= 覆盖"
  fi
  info "二进制: $SEEK_BIN ($("$SEEK_BIN" -version 2>/dev/null || echo version未知))"
  info "日志:   $SEEK_LOG"
  info "目标:   $PLIST_DST"

  local rendered
  rendered=$(mktemp "${TMPDIR:-/tmp}/seek-serve.plist.XXXXXX")
  # 显式带回退出码：bash 3.2 下 EXIT trap 内最后命令的成功码会覆盖
  # 脚本真实退出码（如参数展开错误本应非零退出，却报 0）
  # shellcheck disable=SC2154  # rc 在 trap 执行时赋值，shellcheck 看不到
  # ${rendered:-}：正常返回后 local 已消亡，trap 回到顶层作用域执行
  trap 'rc=$?; rm -f "${rendered:-}"; exit $rc' EXIT
  render_template >"$rendered"
  # 渲染结果自检：占位符漏网或模板被改坏都在这里拦下
  if grep -q '{{SEEK_' "$rendered"; then
    die "渲染后仍有未替换的占位符（模板与脚本不匹配）"
  fi
  plutil -lint "$rendered" >/dev/null || die "渲染结果不是合法 plist"
  info "渲染结果 plutil -lint 通过"

  run mkdir -p "$LAUNCHAGENTS_DIR" "$(dirname "$SEEK_LOG")"
  # 已加载/已存在时先卸旧再装新，保证幂等
  if [ -f "$PLIST_DST" ]; then
    run launchctl unload "$PLIST_DST"
  fi
  run cp "$rendered" "$PLIST_DST"
  run chmod 644 "$PLIST_DST"
  run launchctl load "$PLIST_DST"

  if [ "$DRY_RUN" -eq 1 ]; then
    # bash 3.2：$var 后紧跟全角标点（高位字节）会被吞进变量名，
    # 一律写 ${var} —— 渲染消息里的全角括号同理
    echo "--- 渲染结果（${rendered}）---"
    cat "${rendered}"
    echo "---"
  else
    info "已加载。验证：launchctl list | grep seek-serve"
    info "看启动横幅（含带 token 的 URL）：tail -f $SEEK_LOG"
  fi
}

do_uninstall() {
  # unload 走文件路径；文件已删但 job 仍在时用 remove 按 label 兜底
  if [ -f "$PLIST_DST" ]; then
    run launchctl unload "$PLIST_DST"
  fi
  run launchctl remove "$LABEL" 2>/dev/null || true
  run rm -f "$PLIST_DST"
  info "已卸载（日志保留在 ${SEEK_LOG}，需要可手动删除）"
}

do_status() {
  local line
  if line=$(launchctl list 2>/dev/null | grep "$LABEL"); then
    echo "$line"
    echo "（第二列是最近退出码，第三列是 PID；'-' 表示未在跑）"
  else
    echo "未加载: $LABEL"
    exit 1
  fi
}

CMD="${1:-install}"
[ $# -gt 0 ] && shift
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    *) usage ;;
  esac
done

case "$CMD" in
  install)   do_install ;;
  uninstall) do_uninstall ;;
  status)    do_status ;;
  *)         usage ;;
esac
