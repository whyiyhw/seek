#!/usr/bin/env bash
#
# pitfalls-split.sh — 把旧单文件台账（docs/pitfalls.md / docs/pitfalls-archive.md）
# 拆成「一条目一文件」（卡 c_uxznyn，2026-11-20）。
#
# 为什么拆：单文件是全仓并行合并冲突的头号热点——所有分支往同一文件追加
# （同段末尾相邻行冲突）、头部计数生成块必冲突、triage 挪条目非纯追加。
# 一条目一文件后，并行追加 = 不同路径的新文件，union 合并天然安全；
# 计数由 pitfalls-count.sh 从目录现算，不再入库。
#
# 拆分口径（与 pitfalls-count.sh 旧版一致，唯一真源）：
#   - 跳过 ``` 围栏内的内容（文件头模板里的 `### Short symptom title` 不算）；
#   - `^## ` 切段，段名规范化（kebab-case）成 docs/pitfalls/ 下的目录名；
#   - `^### ` 计条目，正文逐字搬运（标题行起到最后一个非空、非 `---`
#     分隔行止；零内容改动）；
#   - `### [已吸收]` 条目进 docs/pitfalls/absorbed/，文件首行加一条元数据
#     注释（HTML comment）记原段；live 条目不加任何元数据；
#   - slug 由标题生成（小写、非 [a-z0-9] 折叠成 '-'、截 60 字符），
#     冲突加 -2/-3 序号。
#
# 可重复运行（为在途分支准备的收敛路径）：
#   - 对已迁移状态（源文件已是静态说明桩）再跑 = 空操作；
#   - 源文件里与磁盘现存文件逐字节相同的条目 = 跳过（不重写、不改 mtime）；
#   - 只有新条目（或同 slug 不同内容）才落新文件——所以在途分支合并
#     main 后在本分支跑一次本脚本，即可把仍追加在旧文件里的条目转出来。
#
# 拆完自带校验（不通过就不收缩源文件）：
#   ① 每个条目的目标文件读回、absorbed 去元数据行后与源切片 cmp 逐字相等；
#   ② 按段序拼接全部条目切片 == 按同一顺序拼接全部目标文件（去元数据行）。
#
# 用法：
#   scripts/pitfalls-split.sh                    # 拆 docs/pitfalls.md
#   scripts/pitfalls-split.sh <file>             # 拆指定旧台账
#   scripts/pitfalls-split.sh --archive <file>   # 拆归档文件（全部进 absorbed/）
#   scripts/pitfalls-split.sh --dry-run [...]    # 只列出将写出的文件，不写
#   scripts/pitfalls-split.sh --selftest         # 夹具自测
#                （bash scripts/selftest.sh pitfalls-split）
#   位置参数前可用 --dest <dir> 覆盖目标目录（默认 docs/pitfalls；自测用）
#
# bash 3.2 compatible（macOS 自带 3.2；不用 mapfile / 关联数组）。

set -uo pipefail

script_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd) || script_dir="."
repo_root=$(cd "$script_dir/.." 2>/dev/null && pwd) || repo_root="."

usage() {
  sed -n '/^# 用法：/,/^# bash 3.2/p' "$0" | sed 's/^#\{1,\} \{0,1\}//' >&2
  exit 2
}

# --- 规范化 ---------------------------------------------------------------

slugify() { # slugify <text> → stdout
  # 小写、非 [a-z0-9] 与非多字节（≥0x80）字符折叠成 '-'；CJK 保留（纯中文标题
  # 不退化成 entry.md）；不截断——避免按字节截出非法 UTF-8 文件名，冲突由序号兜底。
  # 注意 SET2 只能一个字符（tr 对补集逐字符循环复用最后一个元素，传两个会交替替换）。
  printf '%s' "$1" \
    | LC_ALL=C tr '[:upper:]' '[:lower:]' \
    | LC_ALL=C tr -C 'a-z0-9\200-\377' '-' \
    | LC_ALL=C sed -E 's/-+/-/g; s/^-+//; s/-+$//'
  printf '\n'
}

sec_dir() { # sec_dir <section name> → 目录名（空则 "misc"）
  local d
  d=$(slugify "$1")
  printf '%s' "${d:-misc}"
}

# --- 解析 -----------------------------------------------------------------
# manifest 行：idx<TAB>section<TAB>kind(live|abs)<TAB>title<TAB>start<TAB>end
# end = 条目最后一个非空且非 `---` 分隔行的行号（尾部空行与分隔线不算正文）。
parse_manifest() { # parse_manifest <file> <archive-mode:0|1>
  awk -v archive="$2" '
    function flush() {
      if (title != "") {
        e = (last_entry >= start) ? last_entry : start
        kind = (title ~ /^\[已吸收\]/) ? "abs" : "live"
        sec_out = (archive == 1) ? "-" : sec
        print ++idx "\t" sec_out "\t" kind "\t" title "\t" start "\t" e
      }
      title = ""
    }
    /^[ \t]*```/ { fence = !fence }
    fence { next }
    /^## /  { flush(); sec = substr($0, 4); last_entry = 0; next }
    /^### / { flush(); title = substr($0, 5); start = NR; last_entry = NR; next }
    NF > 0 && $0 !~ /^---$/ { last_entry = NR }
    END { flush() }
  ' "$1"
}

entry_bytes() { # entry_bytes <file> <start> <end> → stdout：逐字切片
  sed -n "$2,$3p" "$1"
}

strip_abs_meta() { # 去掉 absorbed 元数据行（stdin → stdout）
  sed '/^<!-- pitfalls-absorbed: .* -->$/d'
}

# --- 主流程 ---------------------------------------------------------------
# 计划 TSV：idx<TAB>path<TAB>kind<TAB>action(w=write|s=skip)——每条目一行，
# 含跳过的（校验要用它的最终路径）。

run_split() { # run_split <src> <dest> <archive_mode> <dry_run>
  local src=$1 dest=$2 archive_mode=$3 dry_run=$4
  local manifest tsv plan idx sec kind title start end dir base path
  local wrote=0 skipped=0 have want tmp_want fail=0
  [ -f "$src" ] || { echo "pitfalls-split: 文件不存在：$src" >&2; return 2; }

  manifest=$(parse_manifest "$src" "$archive_mode")
  if [ -z "$manifest" ]; then
    echo "pitfalls-split: $src 无条目（已迁移或空文件）——空操作"
    return 0
  fi

  tsv=$(mktemp) || return 3
  plan=$(mktemp) || { rm -f "$tsv"; return 3; }
  tmp_want=$(mktemp) || { rm -f "$tsv" "$plan"; return 3; }
  printf '%s\n' "$manifest" >"$tsv"
  : >"$plan"

  # 逐条目定最终路径：同 slug 已存在且逐字节相同 → skip；不同 → -2/-3 避让。
  while IFS="$(printf '\t')" read -r idx sec kind title start end; do
    if [ "$archive_mode" = 1 ] || [ "$kind" = abs ]; then
      dir=$dest/absorbed
    else
      dir=$dest/$(sec_dir "$sec")
    fi
    base=$(slugify "$title")
    [ -n "$base" ] || base=entry
    # absorbed 文件名去掉「已吸收-」前缀（标题本身保留原样，文件名不重复标记）
    if [ "$kind" = abs ]; then
      base=$(printf '%s\n' "$base" | sed 's/^已吸收-//')
      [ -n "$base" ] || base=absorbed-entry
    fi
    path=$dir/$base.md
    entry_bytes "$src" "$start" "$end" >"$tmp_want"
    if [ "$kind" = abs ]; then
      printf '<!-- pitfalls-absorbed: original-section: %s -->\n' "${sec:--}" | cat - "$tmp_want" >"$tmp_want.2" && mv "$tmp_want.2" "$tmp_want"
    fi
    local n=1
    while [ -f "$path" ]; do
      if cmp -s "$path" "$tmp_want"; then break; fi
      n=$((n + 1))
      path=$dir/$base-$n.md
    done
    if [ -f "$path" ]; then
      skipped=$((skipped + 1))
      printf '%s\t%s\t%s\ts\n' "$idx" "$path" "$kind" >>"$plan"
    else
      wrote=$((wrote + 1))
      printf '%s\t%s\t%s\tw\n' "$idx" "$path" "$kind" >>"$plan"
    fi
  done <"$tsv"

  if [ "$dry_run" = 1 ]; then
    awk -F'\t' '$4 == "w" { printf "would write: %s (%s)\n", $2, $3 }' "$plan"
    printf 'dry-run: would write %s, skip %s identical\n' "$wrote" "$skipped"
    rm -f "$tsv" "$plan" "$tmp_want"
    return 0
  fi

  # 落盘（只写 action=w）
  mkdir -p "$dest/absorbed"
  while IFS="$(printf '\t')" read -r idx path kind action; do
    [ "$action" = w ] || continue
    sec=$(awk -F'\t' -v i="$idx" '$1 == i { print $2; exit }' "$tsv")
    start=$(awk -F'\t' -v i="$idx" '$1 == i { print $5; exit }' "$tsv")
    end=$(awk -F'\t' -v i="$idx" '$1 == i { print $6; exit }' "$tsv")
    mkdir -p "$(dirname "$path")"
    if [ "$kind" = abs ]; then
      {
        printf '<!-- pitfalls-absorbed: original-section: %s -->\n' "${sec:--}"
        entry_bytes "$src" "$start" "$end"
      } >"$path"
    else
      entry_bytes "$src" "$start" "$end" >"$path"
    fi
  done <"$plan"

  # 校验①：目标文件读回（absorbed 去元数据行）与源切片 cmp 逐字相等。
  local tmp_have
  tmp_have=$(mktemp) || tmp_have=/dev/null
  while IFS="$(printf '\t')" read -r idx path kind action; do
    start=$(awk -F'\t' -v i="$idx" '$1 == i { print $5; exit }' "$tsv")
    end=$(awk -F'\t' -v i="$idx" '$1 == i { print $6; exit }' "$tsv")
    strip_abs_meta <"$path" >"$tmp_have"
    entry_bytes "$src" "$start" "$end" | strip_abs_meta | cmp -s - "$tmp_have" || {
      echo "✗ 校验失败：$path 与源切片不一致" >&2
      fail=1
    }
  done <"$plan"
  rm -f "$tmp_have"

  # 校验②：按段序拼源切片 == 按同一顺序拼目标文件。
  if [ "$fail" -eq 0 ]; then
    local from_src from_files
    from_src=$(mktemp) || from_src=/dev/null
    from_files=$(mktemp) || from_files=/dev/null
    while IFS="$(printf '\t')" read -r idx sec kind title start end; do
      entry_bytes "$src" "$start" "$end"
    done <"$tsv" >"$from_src"
    awk -F'\t' '{ print $2 }' "$plan" | while IFS= read -r path; do
      strip_abs_meta <"$path"
    done >"$from_files"
    cmp -s "$from_src" "$from_files" || {
      echo "✗ 校验失败：按段序拼回与源条目部分不一致" >&2
      fail=1
    }
    rm -f "$from_src" "$from_files"
  fi

  rm -f "$tsv" "$plan" "$tmp_want"
  if [ "$fail" -ne 0 ]; then
    echo "pitfalls-split: 校验未过；已写出的新文件无害，源文件未动，可修后重跑" >&2
    return 1
  fi
  printf 'pitfalls-split: %s → %s：写出 %s、跳过相同 %s\n' "$src" "$dest" "$wrote" "$skipped"
  return 0
}

# 源文件收缩为静态说明桩（非 archive 模式、非 dry-run、且确有条目被转出时；
# archive 源不收缩，归档文件的处置由人决定）
write_stub() { # write_stub <src>
  cat >"$1" <<'STUB'
# seek pitfalls log — moved to docs/pitfalls/

One entry per file under [`docs/pitfalls/`](pitfalls/): `<section>/<slug>.md` for live
entries, [`pitfalls/absorbed/`](pitfalls/absorbed/) for superseded ones. The format
template and the policy live in [`docs/pitfalls/README.md`](pitfalls/README.md).

Finding an entry cited elsewhere (most citations say `docs/pitfalls.md "entry title"`):

    grep -rl '<entry title>' docs/pitfalls/

Counts are computed live, never stored: `bash scripts/pitfalls-count.sh` prints them,
`--check` validates entry format. Do NOT append entries to this file — create a new
file under `docs/pitfalls/` instead. A branch that still carries old-format entries
here can run `bash scripts/pitfalls-split.sh` once to convert them.
STUB
}

# --- 自测（夹具） ---------------------------------------------------------

fixture_ledger() { # fixture_ledger <path> [with-fourth:0|1]
  cat >"$1" <<EOF
# fixture ledger

## Format

\`\`\`markdown
### Short symptom title
- **Saw**: template (inside fence, not an entry)
\`\`\`

---

## Alpha / one

### First entry title
- **Saw**: saw one
- **Why**: why one
- **Fix**: fix one
- **Lesson**: lesson one
$( [ "$2" = 1 ] && printf '\n### Fourth entry from an in-flight branch\n- **Saw**: saw four\n- **Why**: why four\n- **Fix**: fix four\n- **Lesson**: lesson four\n' )
---

### 中文注释标题 with ascii tail
- **Saw**: saw two
- **Why**: why two
- **Fix** (variant label): fix two
- **Lesson**: lesson two
- **Refs**: refs two

## Prose only

No entries here.

## Beta

### [已吸收] Old entry gone
- **Saw**: saw old
- **Why**: why old
- **Fix**: fix old
- **Lesson**: lesson old
- **已吸收**: superseded by newer design → sibling entry

### Third entry title
- **Saw**: saw three
- **Why**: why three
- **Fix**: fix three
- **Lesson**: lesson three
EOF
}

cmd_selftest() {
  local d rc=0 root before after
  d=$(mktemp -d) || return 3
  root=$d/fixture
  mkdir -p "$root"

  # 1) 首拆：3 live + 1 absorbed → 4 个文件，逐字搬运，源文件收缩为桩。
  fixture_ledger "$root/pitfalls.md" 0
  bash "$0" --dest "$root/pitfalls" "$root/pitfalls.md" >"$d/o1" 2>&1 \
    || { echo "✗ 自测1：首拆应成功"; sed 's/^/      /' "$d/o1"; rc=1; }
  grep -q '写出 4、跳过相同 0' "$d/o1" || { echo "✗ 自测1：应写出 4 个文件"; sed 's/^/      /' "$d/o1"; rc=1; }
  [ -f "$root/pitfalls/alpha-one/first-entry-title.md" ] || { echo "✗ 自测1：alpha-one 目录缺失"; rc=1; }
  [ -f "$root/pitfalls/alpha-one/中文注释标题-with-ascii-tail.md" ] || { echo "✗ 自测1：CJK 标题应保留在 slug 里"; rc=1; }
  [ -f "$root/pitfalls/beta/third-entry-title.md" ] || { echo "✗ 自测1：beta 目录缺失"; rc=1; }
  [ -f "$root/pitfalls/absorbed/old-entry-gone.md" ] || { echo "✗ 自测1：absorbed 文件缺失"; rc=1; }
  grep -q '^<!-- pitfalls-absorbed: original-section: Beta -->$' "$root/pitfalls/absorbed/old-entry-gone.md" \
    || { echo "✗ 自测1：absorbed 元数据行应记原段 Beta"; rc=1; }
  head -1 "$root/pitfalls/alpha-one/first-entry-title.md" | grep -q '^### First entry title$' \
    || { echo "✗ 自测1：文件首行应为逐字标题"; rc=1; }
  grep -q 'moved to docs/pitfalls/' "$root/pitfalls.md" \
    || { echo "✗ 自测1：源文件应收缩为静态桩"; rc=1; }
  # 逐字搬运抽查：变体 Fix 标签原样保留；条目与条目间的 --- 分隔线不进文件
  grep -q '^- \*\*Fix\*\* (variant label): fix two$' "$root/pitfalls/alpha-one/中文注释标题-with-ascii-tail.md" \
    || { echo "✗ 自测1：变体 Fix 标签未逐字保留"; rc=1; }
  grep -q '^---$' "$root/pitfalls/alpha-one/中文注释标题-with-ascii-tail.md" \
    && { echo "✗ 自测1：条目间 --- 分隔线不应进文件"; rc=1; }
  # 拼回逐字相等（内部校验已跑，这里抽查字节量：切片都非空且带结尾换行）
  [ "$(tail -c 1 "$root/pitfalls/beta/third-entry-title.md" | od -An -c | tr -d ' ')" = '\n' ] \
    || { echo "✗ 自测1：文件应以单个换行结尾"; rc=1; }

  # 2) 幂等：对桩再跑 = 空操作，不产出新文件。
  before=$(find "$root/pitfalls" -name '*.md' | sort | wc -l | tr -d ' ')
  bash "$0" --dest "$root/pitfalls" "$root/pitfalls.md" >"$d/o2" 2>&1 \
    || { echo "✗ 自测2：对桩再跑应成功"; sed 's/^/      /' "$d/o2"; rc=1; }
  grep -q '无条目（已迁移或空文件）——空操作' "$d/o2" \
    || { echo "✗ 自测2：应报告空操作"; sed 's/^/      /' "$d/o2"; rc=1; }
  after=$(find "$root/pitfalls" -name '*.md' | sort | wc -l | tr -d ' ')
  [ "$before" = "$after" ] || { echo "✗ 自测2：空操作不应增减文件（$before → $after）"; rc=1; }

  # 3) 在途分支模拟：旧文件换成「少一条中文、多一条 Fourth」的变体 →
  #    只转出 Fourth，其余跳过；不在源里的旧文件原样保留。
  fixture_ledger "$root/pitfalls.md" 1
  cp "$root/pitfalls/alpha-one/中文注释标题-with-ascii-tail.md" "$d/keep.md"
  bash "$0" --dest "$root/pitfalls" "$root/pitfalls.md" >"$d/o3" 2>&1 \
    || { echo "✗ 自测3：增量再跑应成功"; sed 's/^/      /' "$d/o3"; rc=1; }
  grep -q '写出 1、跳过相同 4' "$d/o3" || { echo "✗ 自测3：应只写出 1 个新文件"; sed 's/^/      /' "$d/o3"; rc=1; }
  [ -f "$root/pitfalls/alpha-one/fourth-entry-from-an-in-flight-branch.md" ] \
    || { echo "✗ 自测3：新条目未转出"; rc=1; }
  cmp -s "$root/pitfalls/alpha-one/中文注释标题-with-ascii-tail.md" "$d/keep.md" \
    || { echo "✗ 自测3：不在源里的既有文件被改动"; rc=1; }
  grep -q 'moved to docs/pitfalls/' "$root/pitfalls.md" \
    || { echo "✗ 自测3：源文件应再次收缩为桩"; rc=1; }

  # 4) 同 slug 不同内容 → 序号避让（模拟 main 侧改过同标题条目）。
  cat >"$d/edited.md" <<'EOF'
## Alpha / one

### First entry title
- **Saw**: EDITED on main
- **Why**: w
- **Fix**: f
- **Lesson**: l
EOF
  bash "$0" --dest "$root/pitfalls" "$d/edited.md" >"$d/o4" 2>&1 \
    || { echo "✗ 自测4：冲突避让应成功"; sed 's/^/      /' "$d/o4"; rc=1; }
  [ -f "$root/pitfalls/alpha-one/first-entry-title-2.md" ] \
    || { echo "✗ 自测4：同 slug 不同内容应落 -2 文件"; sed 's/^/      /' "$d/o4"; rc=1; }
  grep -q 'EDITED on main' "$root/pitfalls/alpha-one/first-entry-title-2.md" \
    || { echo "✗ 自测4：-2 文件内容不符"; rc=1; }
  grep -q 'moved to docs/pitfalls/' "$d/edited.md" \
    || { echo "✗ 自测4：转出后源文件应收缩为桩"; rc=1; }

  # 5) --archive：归档文件全部进 absorbed/，正文去元数据后逐字相等。
  cat >"$d/archive.md" <<'EOF'
# fixture archive — 已吸收条目

本文件收纳已吸收条目。

---

### [已吸收] Archived one
- **Saw**: saw a1
- **Why**: why a1
- **Fix**: fix a1
- **Lesson**: lesson a1
- **已吸收**: gone → somewhere

### [已吸收] Archived two
- **Saw**: saw a2
- **Why**: why a2
- **Fix (root cause)**: fix a2
- **Lesson**: lesson a2
- **已吸收**: gone → elsewhere
EOF
  bash "$0" --dest "$root/pitfalls" --archive "$d/archive.md" >"$d/o5" 2>&1 \
    || { echo "✗ 自测5：归档拆分应成功"; sed 's/^/      /' "$d/o5"; rc=1; }
  [ -f "$root/pitfalls/absorbed/archived-one.md" ] || { echo "✗ 自测5：absorbed/archived-one.md 缺失"; rc=1; }
  [ -f "$root/pitfalls/absorbed/archived-two.md" ] || { echo "✗ 自测5：absorbed/archived-two.md 缺失"; rc=1; }
  grep -q '^<!-- pitfalls-absorbed: original-section: - -->$' "$root/pitfalls/absorbed/archived-one.md" \
    || { echo "✗ 自测5：归档元数据应记 -（无段信息）"; rc=1; }
  grep -q '^- \*\*Fix (root cause)\*\*: fix a2$' "$root/pitfalls/absorbed/archived-two.md" \
    || { echo "✗ 自测5：变体 Fix 标签未逐字保留"; rc=1; }

  # 6) --dry-run：不写任何文件、不动源。
  printf '## Misc\n\n### Dry run only\n- **Saw**: s\n- **Why**: w\n- **Fix**: f\n- **Lesson**: l\n' >"$d/dry.md"
  bash "$0" --dest "$root/pitfalls" --dry-run "$d/dry.md" >"$d/o6" 2>&1 \
    || { echo "✗ 自测6：dry-run 应成功"; rc=1; }
  grep -q 'would write: ' "$d/o6" || { echo "✗ 自测6：dry-run 应列出将写文件"; rc=1; }
  [ ! -f "$root/pitfalls/misc/dry-run-only.md" ] \
    || { echo "✗ 自测6：dry-run 不应写文件"; rc=1; }
  grep -q 'Dry run only' "$d/dry.md" || { echo "✗ 自测6：dry-run 不应动源文件"; rc=1; }

  rm -rf "$d"
  if [ "$rc" -eq 0 ]; then
    echo "pitfalls-split selftest: PASS（首拆/幂等/在途分支增量/冲突避让/归档/dry-run）"
  else
    echo "pitfalls-split selftest: FAIL" >&2
  fi
  return "$rc"
}

# --- 入口 ---------------------------------------------------------------

dest=$repo_root/docs/pitfalls
dry_run=0
archive_mode=0
src=""
mode_selftest=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)  dry_run=1; shift ;;
    --archive)  archive_mode=1; shift
                [ "$#" -gt 0 ] || usage
                src=$1; shift ;;
    --dest)     shift
                [ "$#" -gt 0 ] || usage
                dest=$1; shift ;;
    --selftest) mode_selftest=1; shift ;;
    -h|--help)  usage ;;
    *)          src=$1; shift ;;
  esac
done
[ -n "$src" ] || src=$repo_root/docs/pitfalls.md

if [ "$mode_selftest" = 1 ]; then
  cmd_selftest
  exit $?
fi

run_split "$src" "$dest" "$archive_mode" "$dry_run"
rc=$?
# 非归档源、非 dry-run、且确有条目被转出 → 收缩为桩（空操作时不动）。
# 校验失败（rc=1）时源文件保持原样，等修复后重跑。
if [ "$rc" -eq 0 ] && [ "$dry_run" -eq 0 ] \
   && [ "$archive_mode" -eq 0 ] \
   && [ -n "$(parse_manifest "$src" 0)" ]; then
  write_stub "$src" && echo "pitfalls-split: $src 已收缩为静态说明桩"
fi
exit "$rc"
