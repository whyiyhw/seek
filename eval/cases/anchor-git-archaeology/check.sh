#!/bin/bash
# anchor-git-archaeology check — recompute the introduction SHA
# independently (clone + log -S) and compare with answer.txt.
set -u
command -v git >/dev/null 2>&1 || { echo "check: git required" >&2; exit 1; }
case_dir=$(cd "$(dirname "$0")" && pwd)
[ -f answer.txt ] || { echo "check: answer.txt missing" >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git clone -q "$case_dir/workspace/history.bundle" "$tmp/repo" 2>/dev/null
want=$(git -C "$tmp/repo" log --format=%H -S MAGIC_TOKEN_7f3a9c -- util.py | tail -1)
got=$(tr -d '[:space:]' < answer.txt)
[ -n "$want" ] || { echo "check: could not recompute SHA from bundle" >&2; exit 1; }
[ "$got" = "$want" ] || { echo "check: answer '$got' != recomputed '$want'" >&2; exit 1; }
