#!/usr/bin/env bash
#
# pitfall-to-eval.sh — semi-automatic pitfall → eval-case seeding.
#
# Purpose: the first link of the self-evolution inner loop. Parses
# docs/pitfalls.md entries (template at the head of that file), ranks
# them by how likely they are MODEL-BEHAVIOUR pitfalls the agent can
# still step on (as opposed to one-off code bugs), and scaffolds a
# draft eval/cases/<slug>/ for a picked entry.
#
# This is deliberately SEMI-automatic: the script drafts and triages,
# a human decides. A scaffolded draft contains README.md + prompt.txt
# only — eval/run.sh SKIPS any case without expect.json, so nothing
# runs until a human writes the bounds. That skip is the review gate.
#
# Usage:
#   scripts/pitfall-to-eval.sh                     # list ranked candidates
#   scripts/pitfall-to-eval.sh --list              # same, explicit
#   scripts/pitfall-to-eval.sh --make <line>       # scaffold draft for the
#                                                  # entry starting at <line>
#   scripts/pitfall-to-eval.sh --make <line> <slug>
#   scripts/pitfall-to-eval.sh --make <line> <slug> --force
#
# <line> is the docs/pitfalls.md line number of the `### ` header, as
# printed by --list. <slug> overrides the kebab-case name derived from
# the title.
#
# bash 3.2 compatible (macOS ships 3.2; no mapfile, no assoc arrays).

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
pitfalls="$repo_root/docs/pitfalls.md"
cases_dir="$repo_root/eval/cases"

# --- parse -----------------------------------------------------------------
# Emit one TSV row per entry (tab-separated, tabs stripped from fields):
#   line  category  title  saw  why  fix  lesson  refs
# Entries start at a `### ` header; the file header (Format/Policy, up to
# the `---` rule) and the literal template entry are skipped. Scoring runs
# in the same pass (title+fix+lesson are the behavioural signal).
parse_entries() {
  awk -v OFS='\t' '
    BEGIN { started = 0; score = 0 }
    /^---$/ { if (!started) { started = 1 }; next }
    !started { next }
    /^## /   { cat = $0; sub(/^## /, "", cat); next }
    /^### /  {
      if (title != "" && title != "Short symptom title")
        print line, cat, title, saw, why, fix, lesson, refs, score
      title = $0; sub(/^### /, "", title)
      saw = why = fix = lesson = refs = ""; score = 0
      line = NR
      low = tolower(title)
      if (low ~ /the model/) score += 3
      if (low ~ /llm/) score += 2
      next
    }
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
    END { if (title != "" && title != "Short symptom title")
            print line, cat, title, saw, why, fix, lesson, refs, score }
  ' "$pitfalls"
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
  printf '%s …(clipped, see docs/pitfalls.md)' "${text:0:max}"
}

usage() {
  sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
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
    echo "# columns: score  line(category)  [slug]  title"
    echo "# scaffold with: scripts/pitfall-to-eval.sh --make <line> [slug]"
    echo
    parse_entries | sort -t$'\t' -k9,9rn -k1,1n \
      | while IFS=$'\t' read -r ln cat title _saw _why _fix _lesson _refs score; do
          printf '%3d  %-4d %-28s [%s]\n      %s\n' \
            "$score" "$ln" "($(clip "$cat" 26))" "$(slugify "$title")" "$(clip "$title" 90)"
        done
    ;;
  --make)
    shift
    line_no=${1:-}
    slug=${2:-}
    force=${3:-}
    if [[ -z "$line_no" ]]; then
      echo "pitfall-to-eval: --make needs the entry's line number (see --list)" >&2
      exit 2
    fi
    row=$(parse_entries | awk -F'\t' -v ln="$line_no" '$1 == ln')
    if [[ -z "$row" ]]; then
      echo "pitfall-to-eval: no entry starts at line $line_no of docs/pitfalls.md" >&2
      exit 2
    fi
    IFS=$'\t' read -r ln cat title saw why fix lesson refs _score <<<"$row"
    [[ -z "$slug" ]] && slug=$(slugify "$title")
    case_dir="$cases_dir/$slug"
    if [[ -d "$case_dir" && "$force" != "--force" ]]; then
      echo "pitfall-to-eval: $case_dir already exists (use --force to overwrite drafts)" >&2
      exit 2
    fi
    mkdir -p "$case_dir"

    cat > "$case_dir/README.md" <<EOF
# $slug (DRAFT — expect.json pending human review)

> Scaffolded by \`scripts/pitfall-to-eval.sh\` from \`docs/pitfalls.md\` line $ln
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
