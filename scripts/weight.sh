#!/usr/bin/env bash
#
# weight.sh — the subtraction-mechanism scale (减法机制 L1: 体重秤).
#
# Weighs code and docs, compares doc areas against the byte budgets in
# docs/budget.json, and appends one trend line per run to
# out/weight-<UTC-date>.jsonl. READ-ONLY, REPORT-ONLY: over-budget is
# advice (「SLIM 建议」), never a failure — the hard word freeze for
# AGENTS.md/CLAUDE.md stays with scripts/verify-doc-budgets.sh; this is
# the soft, wider radar around it.
#
# Measured:
#   code  per-package Go LOC (non-test / test) under internal/ and pkg/,
#         plus go.mod direct/indirect dependency counts
#   docs  bytes + lines per area: AGENTS.md, CLAUDE.md, docs/prd (active;
#         docs/prd/archive weighed separately), docs/book, docs/pitfalls.md,
#         CHANGELOG.md
#   drift each area vs docs/budget.json — over → SLIM 建议 line
#
# Budget baseline (see docs/budget.json): actual bytes +10% headroom,
# rounded up to 500 — deliberately loose first, ratchet down later, never
# up silently. Anti-self-consumption precedent: this script budgets
# ITSELF at <300 lines and reports its own line count every run.
#
# Usage: scripts/weight.sh
# Env:   WEIGHT_ROOT    repo root   (default: parent of scripts/)
#        WEIGHT_BUDGET  budget json (default: $WEIGHT_ROOT/docs/budget.json)
#        WEIGHT_OUT     trend dir   (default: $ROOT/out; gitignored)
#
# Bash 3.2 compatible (no assoc arrays, no mapfile); macOS ships 3.2.

set -u

ROOT="${WEIGHT_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BUDGET_FILE="${WEIGHT_BUDGET:-$ROOT/docs/budget.json}"
OUTDIR="${WEIGHT_OUT:-$ROOT/out}"

ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
utc_date="$(date -u +%Y-%m-%d)"

# --- helpers ---------------------------------------------------------------
# Each helper sets globals instead of printing, so call sites stay short
# (bash 3.2 has no local named returns worth the ceremony).

# pkg_loc <dir> -> LOC, TLOC (non-test / test Go lines under dir)
pkg_loc() {
  LOC=$(find "$1" -name '*.go' ! -name '*_test.go' -exec cat {} + 2>/dev/null | wc -l | tr -d ' ')
  TLOC=$(find "$1" -name '*_test.go' -exec cat {} + 2>/dev/null | wc -l | tr -d ' ')
}

# go_mod_deps <go.mod> -> DEP_DIRECT, DEP_INDIRECT
go_mod_deps() {
  DEP_DIRECT=0; DEP_INDIRECT=0
  [ -f "$1" ] || return 0
  block=$(grep -cE '^[[:space:]]*[^ /[:space:]][^[:space:]]*[[:space:]]+v[0-9]' "$1" || true)
  single=$(grep -cE '^require [^ ]+ v[0-9]' "$1" || true)
  ind=$(grep -c '// indirect' "$1" || true)
  total=$((block + single))
  [ "$total" -lt "$ind" ] && total=$ind  # paranoia: never report negatives
  DEP_INDIRECT=$ind
  DEP_DIRECT=$((total - ind))
}

# doc_files <area> -> matching files, one per line, sorted
doc_files() {
  case "$1" in
    docs/prd)         find "$ROOT/docs/prd" -maxdepth 1 -name '*.md' ;;
    docs/prd/archive) find "$ROOT/docs/prd/archive" -name '*.md' ;;
    docs/book)        find "$ROOT/docs/book" -name '*.md' ;;
    *) if [ -f "$ROOT/$1" ]; then printf '%s\n' "$ROOT/$1"; fi ;;
  esac 2>/dev/null | sort
}

# doc_size <area> -> BYTES, LINES (0/0 when nothing matches)
doc_size() {
  BYTES=0; LINES=0
  while IFS= read -r f; do
    b=$(wc -c < "$f" | tr -d ' ')
    l=$(wc -l < "$f" | tr -d ' ')
    BYTES=$((BYTES + b)); LINES=$((LINES + l))
  done < <(doc_files "$1")
}

# budget_of <area> -> BUDGET_BYTES (empty when absent or no budget file).
# budget.json is repo-owned and one-entry-per-line (see its _note), so a
# line-anchored sed is the whole parser; | delimiter because keys hold '/'.
budget_of() {
  BUDGET_BYTES=""
  [ -f "$BUDGET_FILE" ] || return 0
  BUDGET_BYTES=$(sed -n "s|^ *\"$1\": *{ *\"budget_bytes\": *\([0-9][0-9]*\).*|\1|p" "$BUDGET_FILE" | head -n 1)
}

# add_slim <msg> -> appends to the SLIM 建议 stdout list and JSON array
add_slim() {
  slim_stdout="${slim_stdout}$1\\n"
  slim_json="${slim_json}${slim_comma}\"$1\""
  slim_comma=","
  slim_count=$((slim_count + 1))
}

# --- 1. code side ----------------------------------------------------------

echo "weight — code (Go LOC, non-test / test)"
printf '%-28s %8s %9s\n' PACKAGE LOC TEST
code_json=""
code_comma=""
for base in internal pkg; do
  [ -d "$ROOT/$base" ] || continue
  for d in "$ROOT/$base"/*/; do
    [ -d "$d" ] || continue
    pkg="$base/$(basename "$d")"
    pkg_loc "$d"
    printf '%-28s %8s %9s\n' "$pkg" "$LOC" "$TLOC"
    code_json="${code_json}${code_comma}\"$pkg\":{\"loc\":$LOC,\"test_loc\":$TLOC}"
    code_comma=","
  done
done
go_mod_deps "$ROOT/go.mod"
printf '%-28s %8s %9s\n' "go.mod deps" "$DEP_DIRECT" "$DEP_INDIRECT"

# --- 2. doc side -----------------------------------------------------------

AREAS="AGENTS.md CLAUDE.md docs/prd docs/prd/archive docs/book docs/pitfalls.md CHANGELOG.md"

echo
echo "weight — docs (bytes / lines vs $(printf '%s' "$BUDGET_FILE") )"
printf '%-20s %9s %7s %10s %10s\n' AREA BYTES LINES BUDGET STATUS

docs_json=""
docs_comma=""
slim_count=0
slim_comma=""
slim_json=""
slim_stdout=""
for area in $AREAS; do
  doc_size "$area"
  budget_of "$area"
  status="no-budget"; over=0; budget_json="null"
  if [ -n "$BUDGET_BYTES" ]; then
    budget_json="$BUDGET_BYTES"
    if [ "$BYTES" -gt "$BUDGET_BYTES" ]; then
      status="over"; over=$((BYTES - BUDGET_BYTES))
    else
      status="ok"
    fi
  fi
  printf '%-20s %9s %7s %10s %10s\n' "$area" "$BYTES" "$LINES" "${BUDGET_BYTES:--}" "$status"
  docs_json="${docs_json}${docs_comma}\"$area\":{\"bytes\":$BYTES,\"lines\":$LINES,\"budget\":$budget_json,\"status\":\"$status\""
  [ "$over" -gt 0 ] && docs_json="$docs_json,\"over\":$over"
  docs_json="$docs_json}"
  docs_comma=","
  if [ "$over" -gt 0 ]; then
    add_slim "SLIM 建议: $area 超预算 ${over}B（实际 ${BYTES}B / 预算 ${BUDGET_BYTES}B）— 归档、拆分或迁移后收紧"
  fi
done

# self-check: the scale must not outgrow the subtraction it serves
self_lines=$(wc -l < "$0" | tr -d ' ')
if [ "$self_lines" -ge 300 ]; then
  add_slim "SLIM 建议: scripts/weight.sh 自身 ${self_lines} 行 ≥300 — 机制超出防自噬预算，先减它"
fi

# --- 3. report + trend append ----------------------------------------------

echo
if [ -n "$slim_stdout" ]; then
  printf '%b' "$slim_stdout"
else
  echo "all budgeted doc areas within budget"
fi

mkdir -p "$OUTDIR" || { echo "weight: cannot create $OUTDIR" >&2; exit 1; }
trend="$OUTDIR/weight-$utc_date.jsonl"
printf '{"ts":"%s","go_mod":{"direct":%s,"indirect":%s},"code":{%s},"docs":{%s},"self_lines":%s,"over":%s,"slim":[%s]}\n' \
  "$ts" "$DEP_DIRECT" "$DEP_INDIRECT" "$code_json" "$docs_json" "$self_lines" "$slim_count" "$slim_json" >> "$trend"

runs=$(wc -l < "$trend" | tr -d ' ')
echo
echo "trend: $trend (run #$runs on $utc_date UTC)"
