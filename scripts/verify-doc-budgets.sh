#!/usr/bin/env bash
#
# verify-doc-budgets.sh — fail when a budgeted standing doc grows past
# its ceiling.
#
# WHY A MECHANICAL GATE AND NOT JUST REVIEW
#
# AGENTS.md and CLAUDE.md are read at the start of every session: every
# word is paid for in prompt tokens, in every conversation, forever. They
# are also the easiest place to "just add a line", so they accrete. A
# prose rule does not stop that — dsh's own note on this records that
# their accretion happened *while* the current-state rule and reviewer
# attention already existed, which is why they added a gate.
#
# Word count cannot judge quality. That is accepted. What it does is
# force the relocation decision at the exact moment content is being
# added, which is when the author still has the context to place it
# correctly: move the addition to its real home (docs/pitfalls.md, a PRD,
# a reference doc) and leave a pointer, or condense something to pay for
# it.
#
# A budgeted file that goes MISSING also fails, so a rename cannot
# silently orphan its budget.
#
# Usage:
#   scripts/verify-doc-budgets.sh          # check, exit 1 on violation
#   scripts/verify-doc-budgets.sh --report # print the table, always exit 0
#
# Ceilings live in scripts/doc-budgets.tsv (see that file for the ratchet
# rule). Bash 3.2 compatible — no associative arrays, no mapfile; macOS
# ships bash 3.2 and this must run there.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest="$repo_root/scripts/doc-budgets.tsv"

report_only=0
if [ "${1:-}" = "--report" ]; then
  report_only=1
fi

if [ ! -f "$manifest" ]; then
  echo "verify-doc-budgets: manifest not found: $manifest" >&2
  exit 1
fi

failures=0
checked=0

printf '%-16s %8s %8s %8s   %s\n' FILE WORDS CEILING TARGET STATUS

# Read the TSV line by line. `IFS=$'\t'` splits on tabs only, so paths
# with spaces survive. The `|| [ -n "$line" ]` tail catches a final line
# with no trailing newline.
while IFS= read -r line || [ -n "$line" ]; do
  # Skip comments and blank lines.
  case "$line" in
    '#'*) continue ;;
    '') continue ;;
  esac

  path=$(printf '%s' "$line" | cut -f1)
  ceiling=$(printf '%s' "$line" | cut -f2)
  target=$(printf '%s' "$line" | cut -f3)

  if [ -z "$path" ] || [ -z "$ceiling" ]; then
    echo "verify-doc-budgets: malformed manifest line: $line" >&2
    failures=$((failures + 1))
    continue
  fi

  full="$repo_root/$path"
  if [ ! -f "$full" ]; then
    printf '%-16s %8s %8s %8s   %s\n' "$path" "-" "$ceiling" "$target" "MISSING"
    echo "::error file=$path::budgeted document is missing. If it was renamed, update scripts/doc-budgets.tsv in the same change; a rename must not silently orphan its budget." >&2
    failures=$((failures + 1))
    continue
  fi

  words=$(wc -w < "$full" | tr -d ' ')
  checked=$((checked + 1))

  if [ "$words" -gt "$ceiling" ]; then
    over=$((words - ceiling))
    printf '%-16s %8s %8s %8s   %s\n' "$path" "$words" "$ceiling" "$target" "OVER by $over"
    echo "::error file=$path::$path is $words words, over its $ceiling ceiling by $over. Relocate the addition to its real home (docs/pitfalls.md for an incident, a PRD for a design, a reference doc for a table) and leave a pointer — or condense existing prose to pay for it. Raising the ceiling in scripts/doc-budgets.tsv requires explicit justification in the PR." >&2
    failures=$((failures + 1))
  elif [ "$words" -le "$target" ]; then
    # At or below target: the ceiling should ratchet down to ~5% headroom.
    suggested=$((words + words / 20))
    if [ "$ceiling" -gt "$suggested" ]; then
      printf '%-16s %8s %8s %8s   %s\n' "$path" "$words" "$ceiling" "$target" "ok — ratchet ceiling to $suggested"
    else
      printf '%-16s %8s %8s %8s   %s\n' "$path" "$words" "$ceiling" "$target" "ok"
    fi
  else
    headroom=$((ceiling - words))
    printf '%-16s %8s %8s %8s   %s\n' "$path" "$words" "$ceiling" "$target" "frozen — ${headroom}w to ceiling, ${target} is the goal"
  fi
done < "$manifest"

echo
if [ "$report_only" -eq 1 ]; then
  echo "report only ($checked checked, $failures would fail)"
  exit 0
fi

if [ "$failures" -gt 0 ]; then
  echo "verify-doc-budgets: $failures violation(s)" >&2
  exit 1
fi

echo "verify-doc-budgets: $checked document(s) within budget"
