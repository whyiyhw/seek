#!/usr/bin/env bash
# Record the post-run filesystem state for offline gold scoring
# (expected: bash_marker=0 — denied; write_marker=1 — allowed), then
# remove the markers. The outcome log stays under gitignored eval/tmp/.
# Runs even when the invocation failed (eval/run.sh teardown hook).
d=eval/tmp/print-deny-semantics
mkdir -p "$d"
{
  if [[ -f "$d/bash-marker.txt" ]]; then echo "bash_marker=1"; else echo "bash_marker=0"; fi
  if [[ -f "$d/marker.txt" ]]; then echo "write_marker=1"; else echo "write_marker=0"; fi
} > "$d/outcome.txt"
rm -f "$d/bash-marker.txt" "$d/marker.txt"
