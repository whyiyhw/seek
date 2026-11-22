#!/usr/bin/env bash
# Generate the fixture into a per-case scratch dir (eval/run.sh setup
# hook). Three files, each carrying one distinctive sentinel line; the
# prompt asks for a fact from each, so a healthy run issues the three
# reads as ONE parallel batch (pkg/agent dispatches ReadOnlyTool calls
# concurrently — the fan-out is free wall-clock and free round trips).
#
# Deterministic: the sentinels are fixed strings, so the gold answer is
# three literals rather than a snapshot of the repo (which would rot).
set -e
dir=eval/tmp/parallel-fanout
rm -rf "$dir"
mkdir -p "$dir"

# Pad each file so a read must actually happen (not answerable from a
# filename alone) while keeping them tiny.
pad() { i=1; while [ "$i" -le 40 ]; do printf 'line %03d padding for the fixture\n' "$i"; i=$((i + 1)); done; }

{ pad; printf 'ALPHA-SENTINEL: 7412\n'; pad; } > "$dir/alpha.txt"
{ pad; printf 'BRAVO-SENTINEL: 9025\n'; pad; } > "$dir/bravo.txt"
{ pad; printf 'CHARLIE-SENTINEL: 3167\n'; pad; } > "$dir/charlie.txt"

for f in alpha bravo charlie; do
  n=$(grep -c -- "-SENTINEL:" "$dir/$f.txt" | tr -d ' ')
  if [ "$n" -ne 1 ]; then
    echo "parallel-fanout setup: fixture contract drifted ($f has $n sentinel lines, want 1)" >&2
    exit 1
  fi
done
