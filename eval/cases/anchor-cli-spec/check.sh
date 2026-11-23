#!/bin/bash
# anchor-cli-spec check — three graded invocations against golden stdout.
set -u
command -v python3 >/dev/null 2>&1 || { echo "check: python3 required" >&2; exit 1; }
case_dir=$(cd "$(dirname "$0")" && pwd)
[ -f filter_entries.py ] || { echo "check: filter_entries.py missing" >&2; exit 1; }
run() { python3 filter_entries.py "$@" ; }
if ! diff -u "$case_dir/golden/all.txt" <(run); then echo "check: no-args invocation differs" >&2; exit 1; fi
if ! diff -u "$case_dir/golden/alpha50.txt" <(run --min-score 50 --tag alpha); then echo "check: --min-score 50 --tag alpha differs" >&2; exit 1; fi
if ! diff -u "$case_dir/golden/m30.txt" <(run --min-score 30); then echo "check: --min-score 30 differs" >&2; exit 1; fi
# 错误路径：缺 entries.json 时 exit 2、stdout 无输出
tmp=$(mktemp -d); cd "$tmp"
if python3 "$OLDPWD/filter_entries.py" >/dev/null 2>&1; then echo "check: missing entries.json must exit nonzero" >&2; exit 1; fi
