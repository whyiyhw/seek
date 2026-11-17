#!/usr/bin/env bash
#
# kernel-deps.sh — v8 kernel dependency-direction report (docs/prd/v8.md §3
# kernel set, §4 pillar-U first wave; card c_zcygo2). The directional twin of
# weight.sh: REPORT-ONLY and advisory — always exits 0, never a CI gate.
#
# Question answered: which NON-kernel packages of this module do the kernel
# packages import? Every such edge is a direction violation (the kernel is
# meant to stay a closed core). Prints the violation table + count, then
# non-test LOC per kernel package. Errors (go missing, list absent/empty,
# entries that do not resolve) print a loud ERROR line instead of a
# clean-looking report — no silent success.
#
# Kernel set: scripts/kernel-packages.txt (one path relative to the module
# root per line). Production imports only: .Imports excludes _test.go
# imports, so kernel tests borrowing non-kernel helpers do not count (yet).
#
# Usage: scripts/kernel-deps.sh [--self-test]    (run from inside the module)
# Bash 3.2 compatible (no assoc arrays, no mapfile). Budget: <= 120 lines.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIST_FILE="$SCRIPT_DIR/kernel-packages.txt"

# --self-test: build a throwaway module with one manufactured violation and
# assert a copy of this script catches it, plus both loud-error paths.
# Unlike report mode, the self-test exits non-zero on any miss.
self_test() {
  local T rc r1
  T=$(mktemp -d "${TMPDIR:-/tmp}/kdeps.XXXXXX") || { echo "self-test: mktemp failed" >&2; return 1; }
  rc=0
  mkdir -p "$T/kernel" "$T/outside"
  printf 'module selftest.example/kdep\n\ngo 1.21\n' >"$T/go.mod"
  printf 'package kernel\n\nimport _ "selftest.example/kdep/outside"\n' >"$T/kernel/k.go"
  printf 'package outside\n\nvar X = 1\n' >"$T/outside/o.go"
  printf 'kernel\n' >"$T/kernel-packages.txt"
  cp "$0" "$T/kernel-deps.sh"
  ( cd "$T" && bash ./kernel-deps.sh >"$T/o1" 2>&1 ); r1=$?
  grep -q 'kernel -> outside' "$T/o1" || { echo "self-test FAIL: manufactured violation not reported"; rc=1; }
  [ "$r1" = 0 ] || { echo "self-test FAIL: report exited $r1 (must be 0)"; rc=1; }
  printf '# only comments\n' >"$T/kernel-packages.txt"
  ( cd "$T" && bash ./kernel-deps.sh >"$T/o2" 2>&1 )
  grep -q 'ERROR' "$T/o2" || { echo "self-test FAIL: empty list must raise a loud ERROR"; rc=1; }
  ( cd "$T" && env PATH="/nonexistent-kdeps" /bin/bash ./kernel-deps.sh >"$T/o3" 2>&1 )
  grep -q 'ERROR' "$T/o3" || { echo "self-test FAIL: missing go must raise a loud ERROR"; rc=1; }
  rm -rf "$T"
  [ "$rc" = 0 ] && echo "self-test: PASS (violation caught; empty-list & no-go errors loud)"
  return "$rc"
}

case "${1:-}" in
  --self-test) self_test; exit $? ;;
  "") ;;
  *) echo "usage: scripts/kernel-deps.sh [--self-test]" >&2; exit 2 ;;
esac

die() { echo "kernel-deps: ERROR: $1" >&2; exit 0; }  # loud, still exit 0 (advisory)

command -v go >/dev/null 2>&1 || die "'go' not found on PATH — cannot inspect imports"
[ -f "$LIST_FILE" ] || die "kernel list not found: $LIST_FILE"
KERNELS=$(sed -e 's/#.*$//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e 's|^\./||' -e 's|/$||' "$LIST_FILE" | grep -v '^$' || true)
[ -n "$KERNELS" ] || die "kernel list is empty (no entries): $LIST_FILE"
MOD=$(go list -m -f '{{.Path}}' 2>/dev/null) || die "go list -m failed — run inside a Go module"
TMP=$(mktemp) || die "mktemp failed"
trap 'rm -f "$TMP"' EXIT
printf '%s\n' "$KERNELS" >"$TMP"
KPATHS=$(printf '%s\n' "$KERNELS" | sed "s|^|$MOD/|")
NK=$(printf '%s\n' "$KERNELS" | grep -c .)

# a report built on half-resolved packages would lie — refuse to look clean
# flags MUST precede patterns: go list stops flag-parsing at the first
# package argument (a trailing `-f` becomes a package named "-f" — bit me)
GOT=$(go list -e -f '{{.ImportPath}}' $KPATHS 2>/dev/null | grep -c . || true)
[ "$GOT" = "$NK" ] || die "go list resolved $GOT/$NK kernel packages — report suppressed (bad list or broken module?)"
BAD=$(go list -e -f '{{if .Error}}{{.ImportPath}}: {{.Error}}{{end}}' $KPATHS 2>/dev/null | grep . || true)
if [ -n "$BAD" ]; then echo "kernel-deps: ERROR: kernel list entries that did not resolve:" >&2; printf '%s\n' "$BAD" >&2; fi

VIOL=$(go list -e -f '{{.ImportPath}} {{range .Imports}}{{.}} {{end}}' $KPATHS 2>/dev/null |
  awk -v mod="$MOD" -v kf="$TMP" '
    BEGIN { while ((getline l < kf) > 0) kern[mod "/" l] = 1 }
    {
      src = $1
      for (i = 2; i <= NF; i++) {
        p = $i
        if (p == src || p in kern) continue
        if (p != mod && index(p, mod "/") != 1) continue   # keep only in-module
        print substr(src, length(mod) + 2) " -> " substr(p, length(mod) + 2)
      }
    }' | sort -u)

LOC_TABLE=$(go list -e -f '{{range .GoFiles}}{{$.ImportPath}} {{$.Dir}}/{{.}} {{end}}' $KPATHS 2>/dev/null |
  awk -v mod="$MOD" '{ for (i = 2; i <= NF; i++) { f = $i; while ((getline l < f) > 0) c[$1]++; close(f) } }
      END { for (p in c) print substr(p, length(mod) + 2), c[p] }' | sort)
LOC_TOTAL=$(printf '%s\n' "$LOC_TABLE" | awk '{s += $2} END {print s + 0}')

echo "kernel-deps — dependency-direction report (advisory; exit code stays 0)"
echo "module: $MOD   kernel packages: $NK   list: $LIST_FILE"
echo
if [ -n "$VIOL" ]; then
  echo "violations — kernel -> non-kernel in-module import (advice: invert the dep or move the code):"
  printf '%s\n' "$VIOL" | sed 's/^/  /'
else
  echo "violations — none: no kernel package imports a non-kernel package of this module"
fi
echo "violation count: $(printf '%s\n' "$VIOL" | grep -c .)"
echo
echo "kernel LOC (non-test Go files):"
if [ -n "$LOC_TABLE" ]; then
  printf '%s\n' "$LOC_TABLE" | awk '{printf "  %-30s %7d\n", $1, $2}'
  printf '  %-30s %7d\n' total "$LOC_TOTAL"
else
  echo "  (no Go files found under kernel packages)"
fi

exit 0
