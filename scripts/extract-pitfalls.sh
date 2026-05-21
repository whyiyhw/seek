#!/usr/bin/env bash
#
# extract-pitfalls.sh — print every `Pitfall:` trailer in git history.
#
# Purpose: backup signal for docs/pitfalls.md. The Markdown file is the
# primary source of truth (terse, categorised, searchable). The
# `Pitfall:` commit trailer is the auditable trace — if the file ever
# gets out of sync or is lost, this script can recover the rough
# inventory from `git log`.
#
# Output: one line per Pitfall trailer, "[<short-sha>] <YYYY-MM-DD> <summary>"
# in commit order (oldest first). Pipe to grep / less as needed.
#
# Usage:
#   scripts/extract-pitfalls.sh                # all history
#   scripts/extract-pitfalls.sh v0.1.0..HEAD   # since a tag
#   scripts/extract-pitfalls.sh --since=7.days

set -euo pipefail

git log "$@" \
  --reverse \
  --pretty=format:'__COMMIT__%n%h%n%ad%n%B%n__END__' \
  --date=short |
awk '
  /^__COMMIT__$/      { in_commit = 1; getline sha; getline date; next }
  /^__END__$/         { in_commit = 0; next }
  in_commit && /^Pitfall:[[:space:]]*/ {
    sub(/^Pitfall:[[:space:]]*/, "", $0)
    printf "[%s] %s  %s\n", sha, date, $0
  }
'
