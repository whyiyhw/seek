#!/usr/bin/env bash
#
# pkg-inventory.sh — per-package health report for the "consolidate & test" audit.
#
# Purpose: one table that answers "which packages are exposed?" across
# internal/ pkg/ cmd/. Three columns: test-file count, statement coverage,
# and commit activity over the last N months. Coverage is the one column CI
# does NOT already enforce (ci.yml runs vet + -race + build only), so this
# script is the missing measurement.
#
# Reading the output: the table is sorted worst-first — packages with no
# coverage at the top, then lowest coverage upward. A package with LOW-COV
# AND STALE is a consolidation candidate; NO-TESTS on a hot path is a debt
# item; TEST-FAIL means go test could not build/run it at all.
#
# Output columns: PACKAGE  TESTS  COV%  COMMITS  FLAGS
#   FLAGS: NO-TESTS (no _test.go) | LOW-COV (< --min-cov) |
#          STALE (0 commits in window) | TEST-FAIL
#
# Usage:
#   scripts/pkg-inventory.sh                     # default: 6.months window, 50% floor
#   scripts/pkg-inventory.sh --since=3.months    # shorter window
#   scripts/pkg-inventory.sh --min-cov=60        # raise the floor
#   scripts/pkg-inventory.sh --candidates        # only flagged packages
#   scripts/pkg-inventory.sh --json              # JSONL, one object per package
#
# Requires a POSIX-shell environment (Git Bash / WSL / macOS / Linux CI)
# and runs the full test suite once via `go test -cover`.
#
# Exit codes: 0 ok, 1 no packages found, 2 bad argument.

set -euo pipefail

SINCE="6.months"
MIN_COV=50
CANDIDATES=0
JSON=0
for arg in "$@"; do
  case "$arg" in
    --since=*)    SINCE="${arg#--since=}" ;;
    --min-cov=*)  MIN_COV="${arg#--min-cov=}" ;;
    --candidates) CANDIDATES=1 ;;
    --json)       JSON=1 ;;
    -h|--help)    awk 'NR>1 && /^#/ { sub(/^# ?/, ""); print; next } NR>1 { exit }' "$0"; exit 0 ;;
    *) echo "pkg-inventory: unknown argument: $arg" >&2; exit 2 ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v go >/dev/null 2>&1 || { echo "pkg-inventory: 'go' not found on PATH" >&2; exit 1; }

# Windows interop guard: in cygwin/msys shells spawned from cmd, System32's
# sort.exe can shadow POSIX sort (it lacks -t/-k and misreads args). Always
# prefer the POSIX coreutils binary; every supported host has one of these.
if [ -x /usr/bin/sort ]; then SORT=/usr/bin/sort
elif [ -x /bin/sort ]; then SORT=/bin/sort
else SORT=sort; fi

MODULE="$(go list -m -f '{{.Path}}')"

# 1. Enumerate packages and run coverage once for all of them.
PACKAGES="$(go list ./internal/... ./pkg/... ./cmd/...)" || exit 1
COV_OUT="$(mktemp)"
RECORDS="$(mktemp)"
trap 'rm -f "$COV_OUT" "$RECORDS"' EXIT
go test -cover $PACKAGES >"$COV_OUT" 2>&1 || true

# 2. Build one record per package: KEY|PKG|TESTS|COV|COMMITS|FLAGS
#    KEY is the coverage number (or -1 when n/a) so sort puts the worst first.
shopt -s nullglob
while read -r pkg; do
  rel="${pkg#"$MODULE"/}"
  [ "$rel" = "$pkg" ] && rel="$pkg"

  # bash glob instead of `find`: cmd's System32\find.exe is a text filter,
  # not a file lister, and shadows POSIX find in cygwin/msys PATH order.
  set -- "$rel"/*_test.go
  tests=$#
  commits="$(git log --since="$SINCE" --format=%H -- "$rel" 2>/dev/null | wc -l | tr -d ' ' || true)"

  cov="n/a"
  failed=0
  line="$(awk -v p="$pkg" '$1=="ok" && $2==p {print; exit} $1=="FAIL" && $2==p {print; exit} $1=="?" && $2==p {print; exit}' "$COV_OUT")"
  case "$line" in
    ok*)
      cov="$(printf '%s' "$line" | sed -n 's/.*coverage: \([0-9.]*\)%.*/\1/p')"
      [ -z "$cov" ] && cov="n/a" ;;
    FAIL*) failed=1 ;;
  esac

  flags=""
  [ "$tests" = "0" ] && flags="$flags NO-TESTS"
  [ "$failed" = "1" ] && flags="$flags TEST-FAIL"
  if [ "$cov" != "n/a" ] && awk -v c="$cov" -v m="$MIN_COV" 'BEGIN{exit !(c+0 < m)}'; then
    flags="$flags LOW-COV"
  fi
  [ "$commits" = "0" ] && flags="$flags STALE"

  key="$cov"; [ "$key" = "n/a" ] && key="-1"
  printf '%s|%s|%s|%s|%s|%s\n' "$key" "$pkg" "$tests" "$cov" "$commits" "$flags" >>"$RECORDS"
done <<<"$PACKAGES"

# 3. Sort worst-first, then render (table or JSONL, filtered or not).
"$SORT" -t'|' -k1,1n "$RECORDS" | while IFS='|' read -r key pkg tests cov commits flags; do
  if [ "$CANDIDATES" = "1" ] && [ -z "${flags// /}" ]; then
    continue
  fi
  if [ "$JSON" = "1" ]; then
    covjson="$cov"; [ "$covjson" = "n/a" ] && covjson="null"
    if [ -z "${flags// /}" ]; then
      flagsjson="[]"
    else
      flagsjson="[\"$(printf '%s' "$flags" | sed 's/^ *//;s/ /","/g')\"]"
    fi
    printf '{"package":"%s","tests":%s,"coverage":%s,"commits":%s,"flags":%s}\n' \
      "${pkg#"$MODULE"/}" "$tests" "$covjson" "$commits" "$flagsjson"
  else
    covs="$cov"; [ "$covs" = "n/a" ] || covs="$cov%"
    printf '%-58s %6s %9s %8s %s\n' "${pkg#"$MODULE"/}" "$tests" "$covs" "$commits" "$flags"
  fi
done

if [ "$JSON" = "0" ] && [ "$CANDIDATES" = "0" ]; then
  total="$(wc -l <"$RECORDS" | tr -d ' ')"
  no_tests="$(grep -c ' NO-TESTS' "$RECORDS" || true)"
  low_cov="$(grep -c ' LOW-COV' "$RECORDS" || true)"
  stale="$(grep -c ' STALE' "$RECORDS" || true)"
  fail="$(grep -c ' TEST-FAIL' "$RECORDS" || true)"
  echo
  printf 'Summary: %s packages — %s NO-TESTS, %s LOW-COV (<%s%%), %s STALE (no commits in %s), %s TEST-FAIL\n' \
    "$total" "$no_tests" "$low_cov" "$MIN_COV" "$stale" "$SINCE" "$fail"
  echo "Candidates to consolidate: packages flagged LOW-COV AND STALE (run with --candidates to list all flagged)."
fi
