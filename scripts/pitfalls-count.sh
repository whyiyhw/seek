#!/usr/bin/env bash
#
# pitfalls-count.sh — 从 docs/pitfalls/ 目录**现算**台账计数（卡 c_uxznyn，
# 2026-11-20 拆分后取代旧「生成块 + --write」机制，c_554rlg）。
#
# 为什么现算：旧机制把派生计数写进 docs/pitfalls.md 头部生成块，任意两个
# 分支各加一条 → 头部必冲突（拆分动机的三层成因之一）。一条目一文件后
# 计数天然可从目录数出来，入库即负资产：已删除 --write 与生成块。
#
# 口径（与拆分前一致，唯一真源）：
#   - live 条目 = docs/pitfalls/<section>/*.md 的文件数（absorbed/ 除外）；
#   - absorbed 条目 = docs/pitfalls/absorbed/*.md 的文件数；
#   - 有条目的段 = 至少含一个 .md 的段目录；
#   - 四桶 rollup 按段目录名归桶（等价于旧按段名前缀）：
#       tui → TUI；deepseek-api / llm-provider-quirks* / feishu* → API；
#       go / go-language → Go；其余 → tooling & engineering。
#
# 用法：
#   scripts/pitfalls-count.sh              # 报表（advisory，恒 exit 0）
#   scripts/pitfalls-count.sh --check      # 格式校验：每文件有 ### 标题与
#                                          #   Saw/Why/Fix/Lesson 字段、目录名
#                                          #   合法、无重复 slug、absorbed 带
#                                          #   已吸收指针、旧桩未被追加条目
#   scripts/pitfalls-count.sh --selftest   # 夹具自测
#                （bash scripts/selftest.sh pitfalls-count）
#   任一模式后可再跟一个目录路径（默认 docs/pitfalls）
#
# --check 的失败面，都指名道姓：
#   ① 条目文件缺 `### ` 标题行或缺 Saw/Why/Fix/Lesson 任一字段
#      （字段行前缀 `- **Saw` 等，允许 `- **Fix (variant):` 这类变体标签）；
#   ② 目录名不合法（须为小写 kebab / CJK，无空格下划线大写点号）；
#   ③ 同 slug 出现在两个目录（grep 指路会有歧义）；
#   ④ absorbed/ 文件缺 `- **已吸收**` 指针行；
#   ⑤ docs/pitfalls.md 桩里混进了 `### ` 条目（该跑 pitfalls-split.sh）。
#
# 旧机制的「同前缀段」检查（TUI / terminal + TUI / session stats）随段结构
# 一起退役：目录化后新段是显式决定，不再从散文里长回来。
#
# bash 3.2 compatible（macOS 自带 3.2；不用 mapfile / 关联数组）。

set -uo pipefail

usage() {
  sed -n '/^# 用法：/,/^# --check 的失败面/p' "$0" | sed 's/^#\{1,\} \{0,1\}//' | sed '$d' >&2
  exit 2
}

script_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd) || script_dir="."
repo_root=$(cd "$script_dir/.." 2>/dev/null && pwd) || repo_root="."

# --- 计数（现算） ---------------------------------------------------------

# section_tsv <dir> → TSV：段目录<TAB>live（按目录名排序；absorbed 不进表）
section_tsv() {
  local d
  find "$1" -mindepth 1 -maxdepth 1 -type d ! -name absorbed 2>/dev/null | sort \
    | while IFS= read -r d; do
        printf '%s\t%s\n' "$(basename "$d")" "$(find "$d" -name '*.md' | wc -l | tr -d ' ')"
      done
}

absorbed_count() {
  find "$1/absorbed" -name '*.md' 2>/dev/null | wc -l | tr -d ' '
}

rollup() { # rollup <tsv> <tui|api|go|rest>
  awk -F'\t' -v b="$2" '
    function is_tui(n) { return n == "tui" }
    function is_api(n) { return n == "deepseek-api" || n ~ /^llm-provider-quirks/ || n ~ /^feishu/ }
    function is_go(n)  { return n == "go" || n == "go-language" }
    {
      n = $1
      if (b == "tui"  &&  is_tui(n)) s += $2
      if (b == "api"  &&  is_api(n)) s += $2
      if (b == "go"   &&  is_go(n))  s += $2
      if (b == "rest" && !is_tui(n) && !is_api(n) && !is_go(n)) s += $2
    }
    END { print s + 0 }
  ' "$1"
}

# --- 报表 -----------------------------------------------------------------

cmd_report() { # cmd_report <dir> —— advisory，恒 exit 0
  local tsv live absn secs
  [ -d "$1" ] || { echo "pitfalls-count: 目录不存在：$1" >&2; return 0; }
  tsv=$(mktemp) || return 0
  section_tsv "$1" >"$tsv"
  live=$(awk -F'\t' '{ s += $2 } END { print s + 0 }' "$tsv")
  absn=$(absorbed_count "$1")
  secs=$(awk -F'\t' '$2 > 0' "$tsv" | wc -l | tr -d ' ')
  printf 'live=%s absorbed=%s sections_with_entries=%s\n' "$live" "$absn" "$secs"
  printf 'rollup: TUI=%s API=%s Go=%s rest=%s\n' \
    "$(rollup "$tsv" tui)" "$(rollup "$tsv" api)" "$(rollup "$tsv" go)" "$(rollup "$tsv" rest)"
  awk -F'\t' '{ printf "  %-40s %s\n", $1, $2 }' "$tsv"
  rm -f "$tsv"
  return 0
}

# --- 格式校验 -------------------------------------------------------------

errln() { printf '✗ %s\n' "$*" >&2; }

cmd_check() { # cmd_check <dir>
  local dir=$1 rc=0 d f first title
  [ -d "$dir" ] || { errln "${dir}：目录不存在"; return 1; }
  local tsv
  tsv=$(mktemp) || return 3
  section_tsv "$dir" >"$tsv"

  # ② 目录名合法（小写 kebab / CJK；删掉合法字节集后还有剩余即非法）+ 空目录
  while IFS= read -r d; do
    if [ -n "$(printf '%s' "$(basename "$d")" | LC_ALL=C tr -d 'a-z0-9\200-\377-')" ]; then
      errln "目录名不合法（须小写 kebab/CJK，无空格下划线大写点号）：$(basename "$d")"
      rc=1
    fi
    [ "$(find "$d" -name '*.md' | wc -l | tr -d ' ')" = 0 ] \
      && { errln "空段目录：$(basename "$d")"; rc=1; }
  done < <(find "$dir" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort)

  # 非 .md 文件只该出现在根 README.md；根下其它文件告警
  while IFS= read -r f; do
    errln "docs/pitfalls/ 根下只该有 README.md，发现：$f"
    rc=1
  done < <(find "$dir" -mindepth 1 -maxdepth 1 ! -name README.md ! -type d 2>/dev/null)
  while IFS= read -r f; do
    [ "$(basename "$f")" = README.md ] && continue
    errln "段目录里只该有 .md 文件：$f"
    rc=1
  done < <(find "$dir" -mindepth 2 -type f ! -name '*.md' 2>/dev/null)

  # ③ 重复 slug（跨目录同 basename）
  find "$dir" -name '*.md' ! -name README.md 2>/dev/null \
    | awk -F/ '{ if (cnt[$NF]++) print $NF }' | sort -u \
    | while IFS= read -r f; do errln "重复 slug（多目录同名文件）：$f"; done >"$tsv.dup" 2>&1
  #（上面 while 在子 shell 里，改走文件收集）
  if [ -s "$tsv.dup" ]; then cat "$tsv.dup" >&2; rc=1; fi

  # ① 每文件：首行 `### ` 标题 + 四字段；④ absorbed 另需已吸收指针
  while IFS= read -r f; do
    # absorbed 文件首行是元数据注释行，跳过它再取标题
    case $f in
      */absorbed/*) first=$(sed '/^<!-- pitfalls-absorbed: .* -->$/d' "$f" | sed -n '/./{p;q;}') ;;
      *)           first=$(sed -n '/./{p;q;}' "$f") ;;
    esac
    case $first in
      '### '*) : ;;
      *) errln "${f}：首行应为 \`### <标题>\`（现为：${first:0:50}…）"; rc=1 ;;
    esac
    local k
    for k in Saw Why Fix Lesson; do
      grep -q "^- \*\*$k" "$f" || { errln "${f}：缺 \`- **${k}…\` 字段行"; rc=1; }
    done
    case $f in
      */absorbed/*)
        grep -q '^- \*\*已吸收\*\*' "$f" \
          || { errln "${f}：absorbed 条目缺 \`- **已吸收**\` 指针行"; rc=1; }
        head -1 "$f" | grep -q '^<!-- pitfalls-absorbed: original-section: .* -->$' \
          || { errln "${f}：absorbed 条目缺首行元数据注释"; rc=1; }
        ;;
    esac
  done < <(find "$dir" -name '*.md' ! -name README.md 2>/dev/null | sort)

  # ⑤ 旧桩不得再有条目
  local stub=$dir/../pitfalls.md
  if [ -f "$stub" ] && grep -q '^### ' "$stub"; then
    errln "${stub}：静态桩里出现 \`### \` 条目——先跑 scripts/pitfalls-split.sh 转成文件"
    rc=1
  fi

  rm -f "$tsv" "$tsv.dup"
  if [ "$rc" -eq 0 ]; then
    printf 'pitfalls-count: OK（%s）\n' "$dir"
    cmd_report "$dir" | sed -n '1,2p'
  fi
  return "$rc"
}

# --- 自测（夹具） ---------------------------------------------------------

mk_entry() { # mk_entry <path> <title> [missing:Saw|Fix|Lesson]
  mkdir -p "$(dirname "$1")"
  {
    printf '### %s\n' "$2"
    [ "${3:-}" = missing:Saw ]    || printf -- '- **Saw**: s\n'
    printf -- '- **Why**: w\n'
    [ "${3:-}" = missing:Fix ]    || printf -- '- **Fix** (variant ok): f\n'
    [ "${3:-}" = missing:Lesson ] || printf -- '- **Lesson**: l\n'
  } >"$1"
}

cmd_selftest() {
  local d rc=0 dir
  d=$(mktemp -d) || return 3
  dir=$d/pitfalls
  mkdir -p "$dir"

  # 1) 报表：目录现算——live=absorbed=段数=rollup 全对。
  mk_entry "$dir/tui/a.md" "A" ; mk_entry "$dir/tui/b.md" "B"
  mk_entry "$dir/deepseek-api/c.md" "C"
  mk_entry "$dir/go-language/d.md" "D"
  mk_entry "$dir/tooling/e.md" "E"
  mk_entry "$dir/llm-provider-quirks-m6/f.md" "F"
  mkdir -p "$dir/absorbed"
  { printf '<!-- pitfalls-absorbed: original-section: TUI -->\n### [已吸收] G\n'
    printf -- '- **Saw**: s\n- **Why**: w\n- **Fix**: f\n- **Lesson**: l\n- **已吸收**: gone → x\n'; } >"$dir/absorbed/g.md"
  local out
  out=$(bash "$0" "$dir")
  echo "$out" | grep -q 'live=6 absorbed=1 sections_with_entries=5' \
    || { echo "✗ 自测1：报表合计不符"; echo "$out" | sed 's/^/      /'; rc=1; }
  echo "$out" | grep -q 'rollup: TUI=2 API=2 Go=1 rest=1' \
    || { echo "✗ 自测1：rollup 不符"; echo "$out" | sed 's/^/      /'; rc=1; }
  bash "$0" "$dir" >/dev/null 2>&1 || { echo "✗ 自测1：报表应恒 exit 0"; rc=1; }

  # 2) --check 绿：形状完整的目录过。
  bash "$0" --check "$dir" >"$d/o2" 2>&1 \
    || { echo "✗ 自测2：完整目录 --check 应绿"; sed 's/^/      /' "$d/o2"; rc=1; }

  # 3) 缺字段 → 红且指名。
  mk_entry "$dir/tooling/bad.md" "Bad" missing:Lesson
  bash "$0" --check "$dir" >"$d/o3" 2>&1 \
    && { echo "✗ 自测3：缺 Lesson 应红"; rc=1; }
  grep -qF 'bad.md' "$d/o3" && grep -qF 'Lesson' "$d/o3" \
    || { echo "✗ 自测3：报错应指名文件与字段"; sed 's/^/      /' "$d/o3"; rc=1; }
  rm "$dir/tooling/bad.md"

  # 4) 重复 slug（跨目录同 basename）→ 红。
  mk_entry "$dir/go-language/e.md" "E again"
  bash "$0" --check "$dir" >"$d/o4" 2>&1 \
    && { echo "✗ 自测4：重复 slug 应红"; rc=1; }
  grep -q '重复 slug' "$d/o4" || { echo "✗ 自测4：报错应提重复 slug"; sed 's/^/      /' "$d/o4"; rc=1; }
  rm "$dir/go-language/e.md"

  # 5) 目录名不合法 → 红。
  mk_entry "$dir/Bad_Dir/x.md" "X"
  bash "$0" --check "$dir" >"$d/o5" 2>&1 \
    && { echo "✗ 自测5：目录名不合法应红"; rc=1; }
  grep -q '目录名不合法' "$d/o5" || { echo "✗ 自测5：报错应提目录名"; sed 's/^/      /' "$d/o5"; rc=1; }
  rm -rf "$dir/Bad_Dir"

  # 6) 首行不是 ### 标题 → 红。
  printf 'just prose\n' >"$dir/tooling/prose.md"
  bash "$0" --check "$dir" >"$d/o6" 2>&1 \
    && { echo "✗ 自测6：无标题文件应红"; rc=1; }
  grep -q '首行应为' "$d/o6" || { echo "✗ 自测6：报错应提标题行"; sed 's/^/      /' "$d/o6"; rc=1; }
  rm "$dir/tooling/prose.md"

  # 7) absorbed 缺已吸收指针 → 红。
  mk_entry "$dir/absorbed/no-pointer.md" "[已吸收] NoPtr"
  bash "$0" --check "$dir" >"$d/o7" 2>&1 \
    && { echo "✗ 自测7：absorbed 缺指针应红"; rc=1; }
  grep -q '已吸收.*指针' "$d/o7" || { echo "✗ 自测7：报错应提已吸收指针"; sed 's/^/      /' "$d/o7"; rc=1; }
  rm "$dir/absorbed/no-pointer.md"

  # 8) 旧桩里被追加条目 → 红 + 提示跑 split。
  printf '# stub\n\n### sneaky entry\n- **Saw**: s\n' >"$d/pitfalls.md"
  bash "$0" --check "$dir" >"$d/o8" 2>&1 \
    && { echo "✗ 自测8：桩内条目应红"; rc=1; }
  grep -q 'pitfalls-split' "$d/o8" || { echo "✗ 自测8：报错应提示跑 split"; sed 's/^/      /' "$d/o8"; rc=1; }
  printf '# stub, clean\n' >"$d/pitfalls.md"

  # 9) 变体字段标签（- **Fix** (variant ok):）算有字段（自测2 已隐含，单列防回归）。
  mk_entry "$dir/tooling/variant.md" "Variant"
  bash "$0" --check "$dir" >"$d/o9" 2>&1 \
    || { echo "✗ 自测9：变体 Fix 标签应算字段"; sed 's/^/      /' "$d/o9"; rc=1; }

  rm -rf "$d"
  if [ "$rc" -eq 0 ]; then
    echo "pitfalls-count selftest: PASS（报表/绿/缺字段/重复slug/坏目录名/无标题/absorbed指针/桩内条目/变体标签）"
  else
    echo "pitfalls-count selftest: FAIL" >&2
  fi
  return "$rc"
}

# --- 入口 ---------------------------------------------------------------

mode=report
if [ "$#" -gt 0 ]; then
  case "$1" in
    --check)    mode=check;    shift ;;
    --selftest) mode=selftest; shift ;;
    --report)   mode=report;   shift ;;
    -h|--help)  usage ;;
    *)          : ;;
  esac
fi

if [ "$mode" = selftest ]; then
  cmd_selftest
  exit $?
fi

dir=${1:-$repo_root/docs/pitfalls}

case "$mode" in
  report) cmd_report "$dir"; exit 0 ;;
  check)  cmd_check  "$dir"; exit $? ;;
  *)      usage ;;
esac
