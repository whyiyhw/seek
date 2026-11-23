#!/bin/bash
# anchor-fix-failing-tests check — go test is the specification.
set -u
command -v go >/dev/null 2>&1 || { echo "check: go toolchain required" >&2; exit 1; }
# 测试文件不得被改动（与 prompt 的约束一致）：与 fixture 逐字节比对。
case_dir=$(cd "$(dirname "$0")" && pwd)
if ! cmp -s mathutil/mathutil_test.go "$case_dir/workspace/mathutil/mathutil_test.go"; then
  echo "check: mathutil_test.go was modified" >&2
  exit 1
fi
go test ./... >/dev/null 2>&1
