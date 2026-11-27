#!/usr/bin/env bash
#
# pitfall-to-eval.sh — semi-automatic pitfall → eval-case seeding.
#
# Purpose: the first link of the self-evolution inner loop. Reads the
# one-entry-per-file ledger under docs/pitfalls/ (layout since 2026-11-20,
# card c_uxznyn — before that a single docs/pitfalls.md), ranks entries by
# how likely they are MODEL-BEHAVIOUR pitfalls the agent can still step on
# (as opposed to one-off code bugs), and scaffolds a draft eval/cases/<slug>/
# for a picked entry. absorbed/ entries are never candidates.
#
# This is deliberately SEMI-automatic: the script drafts and triages,
# a human decides. A scaffolded draft contains README.md + prompt.txt
# only — eval/run.sh SKIPS any case without expect.json, so nothing
# runs until a human writes the bounds. That skip is the review gate.
#
# Usage:
#   scripts/pitfall-to-eval.sh                     # list ranked candidates
#   scripts/pitfall-to-eval.sh --list              # same, explicit
#   scripts/pitfall-to-eval.sh --make <entry>      # scaffold draft for the
#                                                  # entry file <entry> — a
#                                                  # path under docs/pitfalls/
#                                                  # as printed by --list
#   scripts/pitfall-to-eval.sh --make <entry> <slug>
#   scripts/pitfall-to-eval.sh --make <entry> <slug> --force
#
# <slug> overrides the kebab-case name derived from the title.
#
# bash 3.2 compatible (macOS ships 3.2; no mapfile, no assoc arrays).

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
pitfalls="$repo_root/docs/pitfalls"
cases_dir="$repo_root/eval/cases"

# --- parse -----------------------------------------------------------------
# Emit one TSV row per live entry (tab-separated, tabs stripped from fields):
#   path  category  title  saw  why  fix  lesson  refs  score
# path is relative to docs/pitfalls/ ("<section>/<slug>.md"); category is the
# section directory name. Scoring = model-behaviour likelihood heuristic
# (title + body behavioural signals, unchanged from the single-file era).
parse_entries() {
  local list prog
  list=$(mktemp) || return 0
  # -print0 + xargs -0: entry filenames contain spaces ("体积记账的 delta 锚点 …"),
  # so splitting a newline-separated list into awk's argv truncated paths.
  find "$pitfalls" -name '*.md' ! -name README.md ! -path '*/absorbed/*' -print0 2>/dev/null \
    | sort -z >"$list"
  if [ ! -s "$list" ]; then rm -f "$list"; return 0; fi
  prog='
    FNR == 1 {
      if (title != "") print path, cat, title, saw, why, fix, lesson, refs, score
      title = saw = why = fix = lesson = refs = ""; score = 0
      n = split(FILENAME, p, "/")
      cat = p[n-1]; path = p[n-1] "/" p[n]
      # 不 next：本行就是本文件的 `### ` 标题，交给下面的分支处理
    }
    /^### /  { if (title != "") print path, cat, title, saw, why, fix, lesson, refs, score
               title = $0; sub(/^### /, "", title)
               saw = why = fix = lesson = refs = ""; score = 0
               low = tolower(title)
               if (low ~ /the model/) score += 3
               if (low ~ /llm/) score += 2
               next }
    title == "" { next }
    {
      low = tolower($0)
      # Behavioural signals: does the FIX/LESSON talk about the model,
      # its tool calls, or the instruction surfaces (description/schema/
      # prompt) rather than only about Go code?
      if (low ~ /tool description/) score += 3
      if (low ~ /description/) score += 1
      if (low ~ /schema/) score += 2
      if (low ~ /transcript/) score += 2
      if (low ~ /self-correct/) score += 2
      if (low ~ /the model/) score += 2
      if (low ~ /prompt/) score += 1
      if (low ~ /context (budget|window|fill)/) score += 2
      if (low ~ /retry/) score += 1
      if (low ~ /turn[s]? /) score += 1
    }
    /^- \*\*Saw\*\*: /    { saw = $0;    sub(/^- \*\*Saw\*\*: /, "", saw); gsub(/\t/, " ", saw); next }
    /^- \*\*Why\*\*: /    { why = $0;    sub(/^- \*\*Why\*\*: /, "", why); gsub(/\t/, " ", why); next }
    /^- \*\*Fix\*\*: /    { fix = $0;    sub(/^- \*\*Fix\*\*: /, "", fix); gsub(/\t/, " ", fix); next }
    /^- \*\*Lesson\*\*: / { lesson = $0; sub(/^- \*\*Lesson\*\*: /, "", lesson); gsub(/\t/, " ", lesson); next }
    /^- \*\*Refs\*\*: /   { refs = $0;   sub(/^- \*\*Refs\*\*: /, "", refs); gsub(/\t/, " ", refs); next }
    END { if (title != "") print path, cat, title, saw, why, fix, lesson, refs, score }
  '
  xargs -0 awk -v OFS='\t' "$prog" <"$list"
  rm -f "$list"
}

slugify() {
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//' \
    | cut -c1-50
}

# clip TEXT MAX — first MAX chars on one line, ellipsis if cut.
clip() {
  local text=$1 max=$2
  local n=${#text}
  if (( n <= max )); then printf '%s' "$text"; return; fi
  printf '%s …(clipped — read the entry file)' "${text:0:max}"
}

usage() {
  sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'
}

# --- modes ------------------------------------------------------------------

mode=${1:---list}
case "$mode" in
  -h|--help)
    usage
    exit 0
    ;;
  --list)
    echo "# pitfall → eval candidates (score = model-behaviour likelihood heuristic)"
    echo "# columns: score  (section) path  [slug]  title"
    echo "# scaffold with: scripts/pitfall-to-eval.sh --make <section>/<slug>.md"
    echo
    parse_entries | sort -t$'\t' -k9,9rn -k1,1 \
      | while IFS=$'\t' read -r path cat title _saw _why _fix _lesson _refs score; do
          printf '%3d  (%s) %s [%s]\n      %s\n' \
            "$score" "$(clip "$cat" 26)" "$path" "$(slugify "$title")" "$(clip "$title" 90)"
        done
    ;;
  --make)
    shift
    entry=${1:-}
    slug=${2:-}
    force=${3:-}
    if [[ -z "$entry" ]]; then
      echo "pitfall-to-eval: --make needs an entry path (see --list)" >&2
      exit 2
    fi
    # 归一：允许带 docs/pitfalls/ 前缀；拒绝目录穿越与 absorbed
    entry=${entry#docs/pitfalls/}
    case "$entry" in
      /*|*..*|absorbed/*)
        echo "pitfall-to-eval: 非法条目路径：$entry（须为 <section>/<slug>.md，absorbed 不孵化）" >&2
        exit 2
        ;;
    esac
    row=$(parse_entries | awk -F'\t' -v p="$entry" '$1 == p')
    if [[ -z "$row" ]]; then
      echo "pitfall-to-eval: no live entry at docs/pitfalls/$entry (see --list)" >&2
      exit 2
    fi
    IFS=$'\t' read -r path cat title saw why fix lesson refs _score <<<"$row"
    [[ -z "$slug" ]] && slug=$(slugify "$title")
    case_dir="$cases_dir/$slug"
    if [[ -d "$case_dir" && "$force" != "--force" ]]; then
      echo "pitfall-to-eval: $case_dir already exists (use --force to overwrite drafts)" >&2
      exit 2
    fi
    mkdir -p "$case_dir"

    cat > "$case_dir/README.md" <<EOF
# $slug (DRAFT — expect.json pending human review)

> Scaffolded by \`scripts/pitfall-to-eval.sh\` from \`docs/pitfalls/$path\`
> (category: $cat). \`eval/run.sh\` SKIPS this case until \`expect.json\`
> exists — filling it in is the human review gate, by design.

**Source pitfall**: $title

- **Saw**: $(clip "$saw" 500)
- **Why**: $(clip "$why" 500)
- **Fix**: $(clip "$fix" 500)
- **Lesson**: $(clip "$lesson" 500)
- **Refs**: $(clip "$refs" 300)

## TODO(human): what this case probes

Replace this with the behaviour under test and the failure mode it came
from (see eval/README.md "Adding a case" step 2). State which metric
from run.sh's vocabulary (or a new one) will separate good from bad.

## TODO(human): prompt design

Write the exact \`-p\` input into prompt.txt. Design notes to record here:
what the naive-but-natural first tool call looks like, and what the
healthy path does instead. Do NOT put the desired workflow in the prompt
itself — the case measures unprompted behaviour.

## TODO(human): expect.json

Bounds start from \`{"max_turns": 10}\` and tighten with data. Until this
file exists the runner skips the case — that skip is the gate, so only
add expect.json when the prompt is final.
EOF

    cat > "$case_dir/prompt.txt" <<EOF
TODO(human): replace this placeholder with the exact -p input
(eval/README.md "Adding a case"). The runner skips this case while
expect.json is absent, so this placeholder cannot run.

Source pitfall: $title
Signal to design against: $(clip "$saw" 300)
EOF

    echo "draft scaffolded: $case_dir"
    echo "next steps:"
    echo "  1. fill prompt.txt with the real task"
    echo "  2. rewrite README.md sections TODO(human): …"
    echo "  3. add fixtures + setup.sh/teardown.sh if the case needs scratch files"
    echo "  4. write expect.json LAST — the runner ignores this case until then"
    ;;
  *)
    echo "pitfall-to-eval: unknown mode: $mode (see --help)" >&2
    exit 2
    ;;
esac
