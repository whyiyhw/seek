#!/bin/bash
# anchor-debug-tz check — five fixtures (incl. day/month/year edges) diff
# against golden outputs computed with the plain arithmetic reading.
set -u
command -v python3 >/dev/null 2>&1 || { echo "check: python3 required" >&2; exit 1; }
case_dir=$(cd "$(dirname "$0")" && pwd)
for f in cases/*.txt; do
  name=$(basename "$f" .txt)
  if ! diff -u "$case_dir/golden/${name}.out" <(python3 convert.py "$f" 2>/dev/null); then
    echo "check: $name converts wrong" >&2
    exit 1
  fi
done
