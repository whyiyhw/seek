#!/usr/bin/env bash
# Record the final fixture state for offline correctness scoring, then
# clean up the scratch dir (eval/run.sh teardown hook — runs even when
# the seek invocation failed).
#
# Gold: heavy=1 light=1 lines=13 — the STATUS rule became 24 '━',
# the MENU rule stayed 24 '─', no line added or lost.
set -u
dir=eval/tmp/edit-unicode-read-first
log="$dir/outcome-$(date -u +%Y%m%dT%H%M%S).txt"
if [ -f "$dir/panel.txt" ]; then
  heavy=$(grep -c '^━\{24\}$' "$dir/panel.txt" 2>/dev/null || true)
  light=$(grep -c '^─\{24\}$' "$dir/panel.txt" 2>/dev/null || true)
  heavy=${heavy:-0}
  light=${light:-0}
  lines=$(wc -l < "$dir/panel.txt" | tr -d ' ')
  echo "heavy=$heavy light=$light lines=$lines" > "$log"
  rm -f "$dir/panel.txt"
else
  echo "heavy=0 light=0 lines=0 (fixture missing)" > "$log"
fi
