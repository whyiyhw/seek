#!/bin/bash
# anchor-csv-report check — byte-exact diff against the golden report
# (golden lives OUTSIDE ws so the agent cannot copy it).
set -u
case_dir=$(cd "$(dirname "$0")" && pwd)
[ -f report.tsv ] || { echo "check: report.tsv missing" >&2; exit 1; }
if ! diff -u "$case_dir/golden/report.tsv" report.tsv; then
  echo "check: report.tsv differs from golden" >&2
  exit 1
fi
