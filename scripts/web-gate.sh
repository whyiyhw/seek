#!/usr/bin/env bash
#
# web-gate.sh — the W1.5 zero-build-chain gate (card c_ph2zax,
# feature-web-frontend §4.2/§4.3/§8): two assertions that keep the web
# frontend's asset pipeline free of a node toolchain and inside its
# binary-size budget.
#
#   1. no node build chain  — repo root must carry none of
#      package.json / package-lock.json / yarn.lock / pnpm-lock.yaml /
#      bun.lockb / node_modules. The page is vanilla JS served from
#      go:embed; the day any of these appears, the zero-build-chain
#      premise (§4.2's chosen row) is gone and this gate says so.
#   2. binary size gate     — a linux/amd64 CGO_ENABLED=0 build of
#      ./cmd/seek must not exceed the baseline in
#      scripts/web-size-baseline.txt by more than 200KB. Fail =
#      shrink, or bump the baseline IN THE SAME COMMIT with a
#      justification (the escape hatch for asset-line growth is the
#      `web` build tag, §4.3 — not this ratchet).
#
# Usage:  scripts/web-gate.sh                (gate; exit 1 on trip)
#         scripts/web-gate.sh --update-baseline   (re-measure + rewrite
#          the number; comments in the baseline file survive)
# Env:    WEB_SIZE_BASELINE  baseline file
#                            (default scripts/web-size-baseline.txt; the
#                            override exists so the failure path of this
#                            very gate can be exercised without touching
#                            the real pin)
#
# Bash 3.2 compatible (no assoc arrays, no mapfile); macOS ships 3.2.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASELINE_FILE="${WEB_SIZE_BASELINE:-$ROOT/scripts/web-size-baseline.txt}"
BUDGET=$((200 * 1024))

# ---- 1. no node build chain (repo root) ----
cd "$ROOT"
for f in package.json package-lock.json yarn.lock pnpm-lock.yaml bun.lockb node_modules; do
  if [ -e "$f" ]; then
    echo "::error::$f exists at the repo root — the web frontend is zero-build-chain (feature-web-frontend §4.2); vendor single-file libs instead"
    exit 1
  fi
done
echo "no-node: repo root clean (no package.json / lockfiles / node_modules)"

# ---- 2. binary size gate ----
baseline() {
  # first non-comment line of the baseline file ([[:space:]] not \s —
  # the GNU/mawk regex dialects on ubuntu-latest don't take \s)
  grep -vE '^[[:space:]]*(#|$)' "$BASELINE_FILE" | head -1 | tr -d '[:space:]'
}

build_size() {
  local out
  # not `mktemp -t seek-size`: GNU coreutils reads -t's arg as a
  # TEMPLATE and demands X's ("too few X's") — BSD just prefixes.
  # Path-with-template works everywhere; cf. install-serve.sh:68.
  out="$(mktemp "${TMPDIR:-/tmp}/seek-size.XXXXXX")"
  # shellcheck disable=SC2064  # trap needs the expansion now
  trap "rm -f '$out'" EXIT
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$out" ./cmd/seek
  wc -c < "$out" | tr -d '[:space:]'
}

if [ "${1:-}" = "--update-baseline" ]; then
  size="$(build_size)"
  # rewrite only the number line: comments above it survive verbatim
  awk -v n="$size" '
    !done && $0 !~ /^[[:space:]]*(#|$)/ { print n; done=1; next }
    { print }
  ' "$BASELINE_FILE" > "$BASELINE_FILE.tmp" && mv "$BASELINE_FILE.tmp" "$BASELINE_FILE"
  echo "baseline updated: $size bytes ($(basename "$BASELINE_FILE")) — commit it with the change that grew the binary"
  exit 0
fi

if [ ! -f "$BASELINE_FILE" ]; then
  echo "::error::baseline file $BASELINE_FILE missing — run scripts/web-gate.sh --update-baseline to create history"
  exit 1
fi

base="$(baseline)"
size="$(build_size)"
if [ -z "$base" ] || [ -z "$size" ]; then
  echo "::error::could not read sizes (baseline=$base built=$size)"
  exit 1
fi

over=$(( size - base ))
if [ "$over" -gt "$BUDGET" ]; then
  echo "::error::seek binary grew ${over}B over the baseline ${base}B (budget ${BUDGET}B, feature-web-frontend §4.3): built ${size}B"
  echo "::error::shrink it, or bump the baseline in the same commit: scripts/web-gate.sh --update-baseline + justification"
  exit 1
fi
if [ "$over" -lt -$((100 * 1024)) ]; then
  echo "note: binary is $(( -over ))B BELOW baseline — ratchet it down: scripts/web-gate.sh --update-baseline"
fi
echo "size-gate: built ${size}B, baseline ${base}B, delta ${over}B (budget ${BUDGET}B) — ok"
