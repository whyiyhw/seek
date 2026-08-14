#!/usr/bin/env bash
# eval/tools/compare-read-ab.sh — A/B comparison of read-tool eval results.
#
# Usage:
#   eval/tools/compare-read-ab.sh <baseline-results.jsonl> <improved-results.jsonl>
#       [eval/tmp/write-guard-fallback]      # optional: outcome-log dir for case D
#
# Prints, per case: median metrics, Δ%, realized cost (V4-Flash rack
# rates), gold-answer pass rate, and the pre-registered verdicts from
# docs/test-plan-read-tool.md §7.2. Exit 0 when every applicable
# verdict passes, 1 otherwise.
#
# Rates (USD per 1M tokens, V4-Flash rack — keep in sync with
# internal/pricing; off-peak discount is deliberately NOT applied:
# both sides share it, it cancels in the Δ).
readonly P_MISS=0.14 P_HIT=0.0028 P_OUT=0.28

set -u
BASE=${1:?baseline results file required}
IMPR=${2:?improved results file required}
OUTDIR=${3-}
JQ=$(command -v jq || command -v jq.exe)

[ -f "$BASE" ] || { echo "no such file: $BASE" >&2; exit 2; }
[ -f "$IMPR" ] || { echo "no such file: $IMPR" >&2; exit 2; }

# medians METRIC FILE CASE -> median of .metrics[metric] across the case's rows
medians() {
  "$JQ" -rs --arg m "$1" --arg c "$2" '
    [ .[] | select(.case==$c) | .metrics[$m] // 0 ] | sort
    | if length==0 then "n/a" else .[length/2|floor] end
  ' "$3"
}
# cost_median FILE CASE -> median realized cost in USD
cost_median() {
  "$JQ" -rs --arg c "$2" '
    [ .[] | select(.case==$c)
      | ((.prompt_tokens - .cache_hit_tokens) * 0.14
         + .cache_hit_tokens * 0.0028
         + .completion_tokens * 0.28) / 1000000 ]
    | sort | if length==0 then "n/a" else .[length/2|floor] end
  ' "$1"
}
# gold_rate FILE CASE MARKER -> % of rows whose final_text contains MARKER
gold_rate() {
  "$JQ" -rs --arg c "$2" --arg m "$3" '
    [ .[] | select(.case==$c) | (.final_text // "" | contains($m)) ]
    | if length==0 then "n/a"
      else (([.[] | select(.)] | length) * 100.0 / length) end
  ' "$1"
}
# func_count FILE CASE -> median count (of 6) of gold function names in final_text
# NB: (.final_text // "") MUST be parenthesised before `as` — jq parses
# `a // b as $x | c` as `a // (b as $x | c)` (as swallows the pipeline).
func_count() {
  "$JQ" -rs --arg c "$2" '
    [ .[] | select(.case==$c) | (.final_text // "") as $txt
      | ([ "handleAuth", "parseConfig", "retryOnce",
           "mergeResults", "validateInput", "writeReport"
         ] | map($txt | contains(.)) | map(select(.)) | length) ]
    | sort | if length==0 then "n/a" else .[length/2|floor] end
  ' "$1"
}
# share_eq FILE CASE METRIC VALUE -> % of rows where metric == VALUE
share_eq() {
  "$JQ" -rs --arg c "$2" --arg m "$3" --argjson v "$4" '
    [ .[] | select(.case==$c) | ((.metrics[$m] // 0) == $v) ]
    | if length==0 then "n/a" else (([.[] | select(.)] | length) * 100.0 / length) end
  ' "$1"
}
# share_ge FILE CASE METRIC VALUE -> % of rows where metric >= VALUE
share_ge() {
  "$JQ" -rs --arg c "$2" --arg m "$3" --argjson v "$4" '
    [ .[] | select(.case==$c) | ((.metrics[$m] // 0) >= $v) ]
    | if length==0 then "n/a" else (([.[] | select(.)] | length) * 100.0 / length) end
  ' "$1"
}

pct() { # pct base improved -> Δ% (improved relative to baseline)
  awk -v b="$1" -v i="$2" 'BEGIN{
    if (b=="n/a" || i=="n/a" || b=="null" || i=="null") { print "n/a"; exit }
    if (b==0) { print i==0 ? "0%" : "+inf"; exit }
    printf "%+.0f%%", (i-b)/b*100
  }'
}

declare -a VERDICTS=()
verdict() { VERDICTS+=("$*"); }

echo "baseline: $(basename "$BASE")   improved: $(basename "$IMPR")"
echo
printf '%-22s %-16s %10s %10s %8s\n' case metric baseline improved Δ

for c in read-tail-fact read-eof-blind read-whole-file write-guard-fallback; do
  for m in read_calls probe_reads write_refusals turns completion_tokens; do
    b=$(medians "$m" "$c" "$BASE"); i=$(medians "$m" "$c" "$IMPR")
    d=$(pct "$b" "$i")
    printf '%-22s %-16s %10s %10s %8s\n' "$c" "$m" "$b" "$i" "$d"
  done
  b=$(cost_median "$BASE" "$c"); i=$(cost_median "$IMPR" "$c")
  d=$(pct "$b" "$i")
  printf '%-22s %-16s %10s %10s %8s\n' "$c" "realized_cost_usd" "$b" "$i" "$d"
  case "$c" in
    read-tail-fact)
      b=$(gold_rate "$BASE" "$c" "45s"); i=$(gold_rate "$IMPR" "$c" "45s")
      printf '%-22s %-16s %10s %10s\n' "$c" "gold_45s_pct" "$b" "$i"
      ;;
    read-eof-blind)
      b=$(gold_rate "$BASE" "$c" "LAST_LINE_MARKER=42"); i=$(gold_rate "$IMPR" "$c" "LAST_LINE_MARKER=42")
      printf '%-22s %-16s %10s %10s\n' "$c" "gold_marker_pct" "$b" "$i"
      ;;
    read-whole-file)
      b=$(func_count "$BASE" "$c"); i=$(func_count "$IMPR" "$c")
      printf '%-22s %-16s %10s %10s\n' "$c" "gold_funcs(med/6)" "$b" "$i"
      ;;
    write-guard-fallback)
      b=$(gold_rate "$BASE" "$c" "managed by seek"); i=$(gold_rate "$IMPR" "$c" "managed by seek")
      printf '%-22s %-16s %10s %10s\n' "$c" "gold_content_pct" "$b" "$i"
      ;;
  esac
done

echo
echo "== pre-registered verdicts (docs/test-plan-read-tool.md §7.2) =="

# H1 — read-whole-file: turns −≥30% AND cost −≥20%
bt=$(medians turns read-whole-file "$BASE"); it=$(medians turns read-whole-file "$IMPR")
bc=$(cost_median "$BASE" read-whole-file); ic=$(cost_median "$IMPR" read-whole-file)
awk -v bt="$bt" -v it="$it" -v bc="$bc" -v ic="$ic" 'BEGIN{
  ok = (bt!="n/a" && it!="n/a" && bt>0 && (bt-it)/bt >= 0.30 && bc!="n/a" && ic!="n/a" && bc>0 && (bc-ic)/bc >= 0.20)
  printf "H1 (cost): turns %s->%s, cost %s->%s -> %s\n", bt, it, bc, ic, ok?"PASS":"FAIL"
  exit ok?0:1
}'; verdict "H1 exit $?"

# H2 — read-eof-blind: probe_reads median 0 AND ≥80% of improved runs 0
bp=$(medians probe_reads read-eof-blind "$BASE"); ip=$(medians probe_reads read-eof-blind "$IMPR")
ish=$(share_eq "$IMPR" read-eof-blind probe_reads 0)
awk -v bp="$bp" -v ip="$ip" -v ish="$ish" 'BEGIN{
  ok = (ip=="0" && ish!="n/a" && ish>=80)
  printf "H2 (EOF): probe_reads %s->%s, improved-0-share %s%% -> %s\n", bp, ip, ish, ok?"PASS":"FAIL"
  exit ok?0:1
}'; verdict "H2 exit $?"

# H3 — write-guard-fallback: ≥40% of improved runs see a write refusal
bw=$(medians write_refusals write-guard-fallback "$BASE"); iw=$(medians write_refusals write-guard-fallback "$IMPR")
aw=$(share_ge "$IMPR" write-guard-fallback write_refusals 1)
awk -v bw="$bw" -v iw="$iw" -v aw="$aw" 'BEGIN{
  ok = (aw!="n/a" && aw>=40 && iw>=1)
  printf "H3 (guard): write_refusals %s->%s, refusal-share %s%% -> %s\n", bw, iw, aw, ok?"PASS":"FAIL"
  exit ok?0:1
}'; verdict "H3 exit $?"
if [ -n "$OUTDIR" ] && [ -d "$OUTDIR" ]; then
  total=$(ls "$OUTDIR"/outcome-*.txt 2>/dev/null | wc -l)
  ok=$(grep -l '^content_ok=1' "$OUTDIR"/outcome-*.txt 2>/dev/null | wc -l)
  echo "H3 note: case D content_ok $ok/$total (teardown outcome logs)"
fi

# H5 — read-eof-blind read_calls median == 1
bi=$(medians read_calls read-eof-blind "$IMPR")
awk -v v="$bi" 'BEGIN{ ok=(v=="1"); printf "H5 (paging): read-eof-blind read_calls %s -> %s\n", v, ok?"PASS":"FAIL"; exit ok?0:1 }'; verdict "H5 exit $?"

# Non-regression: turns per case no worse than +30%
FAILREG=0
for c in read-tail-fact read-eof-blind read-whole-file write-guard-fallback; do
  bt=$(medians turns "$c" "$BASE"); it=$(medians turns "$c" "$IMPR")
  awk -v c="$c" -v b="$bt" -v i="$it" 'BEGIN{
    if (b=="n/a" || i=="n/a") exit 0
    if (b==0) { if (i>0) { printf "REGRESSION %s turns %s->%s\n", c, b, i; exit 1 }; exit 0 }
    if ((i-b)/b > 0.30) { printf "REGRESSION %s turns %s->%s\n", c, b, i; exit 1 }
    exit 0
  }' || FAILREG=1
done
if [ "$FAILREG" = "0" ]; then echo "non-regression (turns +<=30%): PASS"; else echo "non-regression (turns +<=30%): FAIL"; fi

echo
echo "L1 tests (H3/H4 deterministic specs) are NOT scored here — run: go test ./internal/tools/read/"
exit $(( FAILREG ))
