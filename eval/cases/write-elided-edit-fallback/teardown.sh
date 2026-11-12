#!/usr/bin/env bash
# Record the final fixture state for offline correctness scoring, then
# clean up the scratch dir (eval/run.sh teardown hook — runs even when
# the seek invocation failed).
#
# Gold: retention14=1 retention30=0 lines=100 — the knob flipped, the
# `30` variant is gone, and no line was added or lost (a whole-file
# write would have collapsed the elided middle).
set -u
dir=eval/tmp/write-elided-edit-fallback
log="$dir/outcome-$(date -u +%Y%m%dT%H%M%S).txt"
if [ -f "$dir/config-dump.txt" ]; then
  r14=$(grep -c 'retention_days: 14' "$dir/config-dump.txt" 2>/dev/null || true)
  r30=$(grep -c 'retention_days: 30' "$dir/config-dump.txt" 2>/dev/null || true)
  r14=${r14:-0}
  r30=${r30:-0}
  lines=$(wc -l < "$dir/config-dump.txt" | tr -d ' ')
  echo "retention14=$r14 retention30=$r30 lines=$lines" > "$log"
  rm -f "$dir/config-dump.txt"
else
  echo "retention14=0 retention30=0 lines=0 (fixture missing)" > "$log"
fi
