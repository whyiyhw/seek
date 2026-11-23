#!/bin/bash
# anchor-refactor-shared-helper check — behavior pinned by tests (byte
# comparison against the untouched fixture) + the refactor actually
# happened (a func sharedNormalize declaration exists).
set -u
command -v go >/dev/null 2>&1 || { echo "check: go toolchain required" >&2; exit 1; }
case_dir=$(cd "$(dirname "$0")" && pwd)
if ! cmp -s normalize/normalize_test.go "$case_dir/workspace/normalize/normalize_test.go"; then
  echo "check: normalize_test.go was modified" >&2
  exit 1
fi
if ! grep -q 'func sharedNormalize' normalize/*.go; then
  echo "check: no func sharedNormalize declaration found" >&2
  exit 1
fi
go test ./... >/dev/null 2>&1
