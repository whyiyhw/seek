#!/usr/bin/env bash
# Generate the fixture into a per-case scratch dir (eval/run.sh setup
# hook). Deterministic; NOT committed as testdata — a 550 KiB log does
# not belong in git, and the generator documents the arithmetic.
#
# Fixture contract (sanity-checked at the end):
#   - lines 1..2000: every 4th line is lvl=ERROR with code=ERR-(i/4 mod
#     10) -> 500 ERROR lines, 50 per code ERR-00..ERR-09
#   - appended: 60x ERR-03, 36x ERR-07, 18x ERR-05
#     -> totals ERR-03=110, ERR-07=86, ERR-05=68; grand total 614
#   - noise lines contain no "error"/"ERROR"/"ERR-" tokens
#   - every line ~260 chars so a default grep (20 matches, context 3,
#     merged blocks) overflows the tool's 16 KiB output cap
set -e
dir=eval/tmp/grep-refine-not-flood
mkdir -p "$dir"
f="$dir/app.log"
: > "$f"

noise() { # $1=line no, $2=lvl
  printf '2026-11-12T%02d:%02d:%02d.%03dZ lvl=%-5s svc=api req=%07d shard=%02d region=ap-southeast-1 trace=4f%05xabcdef0123456789 conn=keep-alive bytes_in=%05d bytes_out=%05d queue_wait_ms=%03d handler=dispatch.middleware.chain tenant=acme-%03d session=sess-%06d x_forwarded_for=10.%d.%d.%d user_agent=svc-mesh/2.%d\n' \
    $(( ($1 / 3600) % 24 )) $(( ($1 / 60) % 60 )) $(( $1 % 60 )) $(( ($1 * 17) % 1000 )) \
    "$2" "$(( $1 * 31 % 9999999 + 1 ))" $(( $1 % 16 )) \
    $(( ($1 * 37) % 99999 )) $(( ($1 * 53) % 99999 )) $(( ($1 * 11) % 900 )) \
    $(( $1 % 500 )) $(( ($1 * 7) % 999999 )) \
    $(( $1 % 12 )) $(( ($1 * 5) % 250 )) $(( ($1 * 3) % 250 )) $(( $1 % 9 ))
}

errline() { # $1=line no, $2=code
  printf '2026-11-12T%02d:%02d:%02d.%03dZ lvl=ERROR svc=api req=%07d code=%s shard=%02d region=ap-southeast-1 trace=4f%05xabcdef0123456789 downstream=deepseek latency_ms=%04d attempt=%d/3 msg=request failed after retry budget exhausted endpoint=/v1/chat/completions will_drop=true backoff_ms=%05d correlation_id=corr-%08d tenant=acme-%03d session=sess-%06d bytes_in=%05d bytes_out=%05d\n' \
    $(( ($1 / 3600) % 24 )) $(( ($1 / 60) % 60 )) $(( $1 % 60 )) $(( ($1 * 17) % 1000 )) \
    "$(( $1 * 31 % 9999999 + 1 ))" "$2" $(( $1 % 16 )) \
    $(( ($1 * 97) % 99999 )) $(( ($1 * 13) % 4000 )) $(( ($1 % 3) + 1 )) \
    $(( ($1 * 29) % 30000 )) $(( $1 * 131 % 99999999 )) \
    $(( $1 % 500 )) $(( ($1 * 7) % 999999 )) \
    $(( ($1 * 37) % 99999 )) $(( ($1 * 53) % 99999 ))
}

i=1
while [ "$i" -le 2000 ]; do
  if [ $(( i % 4 )) -eq 0 ]; then
    errline "$i" "$(printf 'ERR-%02d' $(( (i / 4) % 10 )) )" >> "$f"
  elif [ $(( i % 3 )) -eq 0 ]; then
    noise "$i" DEBUG >> "$f"
  else
    noise "$i" INFO >> "$f"
  fi
  i=$((i + 1))
done

j=0
while [ "$j" -lt 60 ]; do errline "$((2001 + j))" ERR-03 >> "$f"; j=$((j + 1)); done
j=0
while [ "$j" -lt 36 ]; do errline "$((2061 + j))" ERR-07 >> "$f"; j=$((j + 1)); done
j=0
while [ "$j" -lt 18 ]; do errline "$((2097 + j))" ERR-05 >> "$f"; j=$((j + 1)); done

# Sanity-check the contract; fail loudly if it drifted.
total=$(grep -c 'lvl=ERROR' "$f" | tr -d ' ')
e03=$(grep -c 'code=ERR-03' "$f" | tr -d ' ')
lines=$(wc -l < "$f" | tr -d ' ')
if [ "$total" -ne 614 ] || [ "$e03" -ne 110 ] || [ "$lines" -ne 2114 ]; then
  echo "grep-refine-not-flood setup: fixture contract drifted (total=$total e03=$e03 lines=$lines)" >&2
  exit 1
fi
