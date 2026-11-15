#!/usr/bin/env bash
#
# eval-trend.sh — eval baseline trend radar (eval 基线趋势).
#
# Aggregates eval/results/*.jsonl into a per-case trend table:
#   - last-N pass rate (N = TREND_WINDOW, default 5)
#   - first-failure time (earliest recorded failing run)
#   - consecutive-failure count (streak, counting back from the latest run)
#   - drift-suspect flags, e.g. bash-overreach's shape: a case nobody ran
#     for months fails the moment it is finally exercised, so the failure
#     date is only an UPPER BOUND on when the behaviour actually drifted.
#
# Flag vocabulary (also in the report footer):
#   failing                latest run failed
#   suspect:gap-uncovered  latest run failed AND the previous run of that
#                          case was > TREND_GAP_DAYS earlier — drift could
#                          have crept in any time inside the uncovered gap
#   suspect:no-baseline    latest run failed AND the case has ≤ window runs
#                          AND its first recorded run is < TREND_GAP_DAYS old
#                          (new / long-unrun case first exposed by a failure)
#   suspect:streak         ≥ 2 consecutive failures — sustained, not a fluke
#   stale-coverage         no run at all for > TREND_GAP_DAYS (even passing)
#
# Reader contract: rows are extracted with jq, NOT line-by-line — the early
# result files (pre v0.5) hold pretty-printed concatenated JSON objects, one
# result spanning many lines; only files from the current run.sh are strict
# one-object-per-line JSONL. Rows missing `ran_at` fall back to the UTC date
# prefix of the filename. eval/cases/<name>/ dirs with zero recorded runs are
# reported as never-run coverage holes.
#
# READ-ONLY over eval/results; writes only the two report artifacts.
#
# Usage: scripts/eval-trend.sh [--selftest]
# Env:   TREND_ROOT       repo root            (default: parent of scripts/)
#        TREND_RESULTS    results dir          (default: $ROOT/eval/results)
#        TREND_CASES      cases dir            (default: $ROOT/eval/cases)
#        TREND_OUT_MD     report md            (default: $RESULTS/trend-report.md)
#        TREND_OUT_JSON   report json          (default: $RESULTS/trend-report.json)
#        TREND_WINDOW     last-N window        (default 5)
#        TREND_GAP_DAYS   drift gap threshold  (default 30)
#        TREND_NOW        report clock         (default: now, UTC; override
#                                               for reproducible reports)
#
# Bash 3.2 compatible (no assoc arrays, no mapfile); macOS ships 3.2.
# jq is required, same as eval/run.sh.

set -u

# ---------------------------------------------------------------- config

script_dir=$(cd "$(dirname "$0")" && pwd)
TREND_ROOT=${TREND_ROOT:-$script_dir/..}
TREND_RESULTS=${TREND_RESULTS:-$TREND_ROOT/eval/results}
TREND_CASES=${TREND_CASES:-$TREND_ROOT/eval/cases}
TREND_OUT_MD=${TREND_OUT_MD:-$TREND_RESULTS/trend-report.md}
TREND_OUT_JSON=${TREND_OUT_JSON:-$TREND_RESULTS/trend-report.json}
TREND_WINDOW=${TREND_WINDOW:-5}
TREND_GAP_DAYS=${TREND_GAP_DAYS:-30}
TREND_NOW=${TREND_NOW:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}

if command -v jq >/dev/null 2>&1; then
  JQ=jq
elif command -v jq.exe >/dev/null 2>&1; then
  JQ=jq.exe   # Windows Git-Bash; see eval/run.sh's jq resolution notes
else
  echo "eval-trend: jq is required but not installed" >&2
  exit 1
fi

# jq's @tsv escapes tab/newline inside values; case names and ISO timestamps
# contain neither, so the TSV round-trip below is safe.

# ---------------------------------------------------------------- collect

# collect_rows OUT_TSV — jq-extract (ts, case, pass, file) from every
# *.jsonl, fill missing ran_at from the filename's UTC-date prefix, sort.
collect_rows() {
  local out=$1 f base filedate n_all
  : > "$out"
  files_scanned=0; files_warned=0; sum_objects=0

  for f in "$TREND_RESULTS"/*.jsonl; do
    [ -e "$f" ] || continue
    base=$(basename "$f")
    files_scanned=$((files_scanned + 1))

    filedate=""
    case $base in
      [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]-*)
        filedate="${base:0:10}T00:00:00Z" ;;
    esac

    # Slurp count first: a parse failure here means the whole file is bad.
    n_all=$($JQ -s 'length' "$f" 2>/dev/null)
    if [ -z "$n_all" ]; then
      echo "eval-trend: WARN unparseable $base — file skipped" >&2
      files_warned=$((files_warned + 1))
      continue
    fi
    sum_objects=$((sum_objects + n_all))

    # -r is load-bearing: @tsv without raw output emits a JSON-encoded
    # string (tabs escaped as \t) and the downstream TSV split dies.
    $JQ -r 'select((.case|type)=="string" and (.pass|type)=="boolean")
            | [(.ran_at // ""), .case, (if .pass then 1 else 0 end)] | @tsv' \
      "$f" 2>/dev/null \
      | awk -v fs="$filedate" -v fb="$base" 'BEGIN { FS=OFS="\t" }
          { ts=$1
            if (ts == "") { if (fs == "") next; ts=fs }   # no ran_at, no file date
            print ts, $2, $3, fb, NR }' >> "$out"
  done

  LC_ALL=C sort -t'	' -k1,1 -k2,2 -k4,4 -k5,5n -s "$out" -o "$out"
  rows_kept=$(wc -l < "$out" | tr -d ' ')
  rows_total=$sum_objects
  rows_skipped=$((sum_objects - rows_kept))
}

# ---------------------------------------------------------------- aggregate

# aggregate_rows TSV — one awk pass builds BOTH artifacts: the MD table with
# suspect sections, and the JSON. Values are constrained (kebab case names,
# ISO timestamps, flag literals), so printf-assembling the JSON is safe.
aggregate_rows() {
  local tsv=$1 never_run="" c
  if [ -d "$TREND_CASES" ]; then
    for c in "$TREND_CASES"/*/; do
      [ -d "$c" ] || continue
      c=$(basename "$c")
      # a case dir without expect.json never runs (scaffold gate, eval/README)
      [ -f "$TREND_CASES/$c/expect.json" ] || continue
      never_run="$never_run $c"
    done
  fi

  awk -v md="$TREND_OUT_MD" -v json="$TREND_OUT_JSON" \
      -v gen="$TREND_NOW" -v src="$TREND_RESULTS" \
      -v window="$TREND_WINDOW" -v gapth="$TREND_GAP_DAYS" \
      -v nfiles="$files_scanned" -v nwarn="$files_warned" \
      -v nrows="$rows_total" -v nskip="$rows_skipped" \
      -v never="$never_run" '
    function daynum(s,  y, m, d) {          # epoch days from YYYY-MM-DD[..]
      y = substr(s,1,4)+0; m = substr(s,6,2)+0; d = substr(s,9,2)+0
      if (m <= 2) { y--; m += 12 }
      return 365*y + int(y/4) - int(y/100) + int(y/400) \
             + int((153*(m-3)+2)/5) + d - 719469
    }
    function d(s) { return substr(s,1,10) } # date-only for narrow columns
    BEGIN { FS = "\t"; nowd = daynum(gen) }
    {
      c = $2
      if (!(c in seen)) { seen[c] = 1; ord[++nc] = c }
      n[c]++
      T[c, n[c]] = $1; P[c, n[c]] = ($3 == "1") ? 1 : 0; F[c, n[c]] = $4
    }
    END {
      # ---- pass 1: stats + flags
      wcas = 4
      for (i = 1; i <= nc; i++) {
        c = ord[i]; m = n[c]
        if (length(c) > wcas) wcas = length(c)
        passed[c] = 0; firstfail[c] = ""
        for (j = 1; j <= m; j++) {
          if (P[c, j]) passed[c]++
          else if (firstfail[c] == "") firstfail[c] = T[c, j]
        }
        failed[c] = m - passed[c]
        st = 0
        for (j = m; j >= 1 && P[c, j] == 0; j--) st++
        streak[c] = st
        lastrun[c] = T[c, m]; lastfile[c] = F[c, m]
        prevrun[c] = (m >= 2) ? T[c, m-1] : ""
        idle[c] = nowd - daynum(lastrun[c])
        gapd[c] = (m >= 2) ? daynum(lastrun[c]) - daynum(prevrun[c]) : -1
        nf = 0
        failing[c] = (st >= 1)
        if (failing[c]) fl[c, ++nf] = "failing"
        if (failing[c] && gapd[c] > gapth) fl[c, ++nf] = "suspect:gap-uncovered"
        if (failing[c] && m <= window && nowd - daynum(T[c,1]) <= gapth)
          fl[c, ++nf] = "suspect:no-baseline"
        if (st >= 2) fl[c, ++nf] = "suspect:streak"
        if (idle[c] > gapth) fl[c, ++nf] = "stale-coverage"
        nflags[c] = nf
        nsuspect[c] = 0
        for (j = 1; j <= nf; j++) if (fl[c, j] ~ /^suspect:/) nsuspect[c]++
        if (failing[c]) sum_fail++
        if (nsuspect[c] > 0) sum_susp++
        if (idle[c] > gapth && !failing[c]) sum_stale++
      }
      # case-dir candidates minus cases that HAVE recorded runs
      ntmp = split(never, cand, " ")
      nnr = 0
      for (i = 1; i <= ntmp; i++) if (!(cand[i] in seen)) nrv[++nnr] = cand[i]

      # ---- markdown
      print "# eval trend report" > md
      printf "generated: %s   source: %s (%d files, %d rows, %d skipped, %d warned)\n", gen, src, nfiles, nrows, nskip, nwarn > md
      printf "window: last %d runs · gap threshold: %d days\n\n", window, gapth > md
      printf "summary: %d cases · %d failing · %d drift suspects · %d stale coverage · %d never-run\n\n", nc, sum_fail, sum_susp, sum_stale, nnr > md
      printf "| %-*s | runs | last-%d | streak | first fail | last run | idle d | flags |\n", wcas, "case", window > md
      printf "| %-*s | ---: | ---: | ---: | --- | --- | ---: | --- |\n", wcas, ":--" > md
      for (i = 1; i <= nc; i++) {
        c = ord[i]; fstr = ""
        for (j = 1; j <= nflags[c]; j++) fstr = fstr ((j > 1) ? ", " : "") fl[c, j]
        if (fstr == "") fstr = "-"
        ln = (m2 = n[c]) < window ? m2 : window
        lnp = 0
        for (j = n[c] - ln + 1; j <= n[c]; j++) lnp += P[c, j]
        ff = (firstfail[c] == "") ? "-" : d(firstfail[c])
        printf "| %-*s | %d | %d/%d | %d | %s | %s | %d | %s |\n", \
               wcas, c, n[c], lnp, ln, streak[c], ff, d(lastrun[c]), idle[c], fstr > md
      }
      print "" > md
      if (sum_susp + sum_fail > 0) {
        print "## drift suspects / failing" > md
        for (i = 1; i <= nc; i++) {
          c = ord[i]
          if (!failing[c] && nsuspect[c] == 0) continue
          printf "- **%s** —", c > md
          for (j = 1; j <= nflags[c]; j++) printf " `%s`", fl[c, j] > md
          printf ": " > md
          sep = ""
          if (failing[c] && gapd[c] > gapth) {
            printf "%slast PASS-coverage was %d d before the failing run — drift could have crept in any time in that uncovered gap", sep, gapd[c] > md
            sep = "; "
          }
          for (j = 1; j <= nflags[c]; j++) {
            if (fl[c, j] == "suspect:no-baseline") {
              printf "%sfirst recorded run %s is within %d d of now and already failing — no historical baseline", sep, d(T[c,1]), gapth > md
              sep = "; "
            }
            if (fl[c, j] == "suspect:streak") {
              printf "%s%d consecutive failures — sustained, not a one-off", sep, streak[c] > md
              sep = "; "
            }
          }
          if (failing[c] && gapd[c] <= gapth && nsuspect[c] == 0) {
            printf "%sfailing with recent coverage — check expect.json vs the run", sep > md
          }
          printf " (latest run: %s from `%s`)\n", d(lastrun[c]), lastfile[c] > md
        }
        print "" > md
      }
      if (sum_stale > 0) {
        print "## stale coverage (passing but idle > gap threshold)" > md
        for (i = 1; i <= nc; i++) {
          c = ord[i]
          if (failing[c] || idle[c] <= gapth) continue
          printf "- **%s** — last run %s, idle %d d: contract unexercised recently\n", c, d(lastrun[c]), idle[c] > md
        }
        print "" > md
      }
      if (nnr > 0) {
        print "## never-run cases (case dir exists, zero recorded runs)" > md
        for (i = 1; i <= nnr; i++)
          printf "- **%s** — no baseline at all; first run may surprise (bash-overreach shape)\n", nrv[i] > md
        print "" > md
      }
      print "---" > md
      printf "flags: failing=latest run failed · suspect:gap-uncovered=fail after >%dd uncovered · suspect:no-baseline=≤%d runs, first run <%dd old, failing · suspect:streak=≥2 consecutive fails · stale-coverage=idle >%dd\n", gapth, window, gapth, gapth > md
      print "note: a failure date after a coverage gap is only an UPPER BOUND on when the behaviour drifted. Rows are jq-parsed (old files are pretty-printed concatenated objects, not one-per-line JSONL); rows missing ran_at fall back to the filename UTC date." > md

      # ---- json
      print "{" > json
      printf "  \"generated_at\": \"%s\",\n", gen > json
      printf "  \"results_dir\": \"%s\",\n", src > json
      printf "  \"window\": %d,\n  \"gap_days\": %d,\n", window, gapth > json
      printf "  \"files_scanned\": %d,\n  \"files_warned\": %d,\n  \"rows\": %d,\n  \"rows_skipped\": %d,\n", nfiles, nwarn, nrows, nskip > json
      printf "  \"summary\": {\"cases\": %d, \"failing\": %d, \"drift_suspects\": %d, \"stale_coverage\": %d, \"never_run\": %d},\n", nc, sum_fail, sum_susp, sum_stale, nnr > json
      print "  \"cases\": [" > json
      for (i = 1; i <= nc; i++) {
        c = ord[i]
        ln = (n[c] < window) ? n[c] : window
        lnp = 0
        for (j = n[c] - ln + 1; j <= n[c]; j++) lnp += P[c, j]
        fstr = ""
        for (j = 1; j <= nflags[c]; j++) fstr = fstr ((j > 1) ? ", " : "") "\"" fl[c, j] "\""
        printf "    {\"case\": \"%s\", \"runs\": %d, \"passed\": %d, \"failed\": %d, \"last_n_runs\": %d, \"last_n_passed\": %d, \"last_n_rate\": %.2f, \"streak_failures\": %d, \"first_fail\": %s, \"first_run\": \"%s\", \"last_run\": \"%s\", \"idle_days\": %d, \"gap_days\": %s, \"flags\": [%s]}%s\n", \
               c, n[c], passed[c], failed[c], ln, lnp, lnp/ln, streak[c], \
               (firstfail[c] == "" ? "null" : "\"" firstfail[c] "\""), \
               T[c,1], lastrun[c], idle[c], \
               (gapd[c] < 0 ? "null" : gapd[c]), fstr, (i < nc ? "," : "") > json
      }
      print "  ]," > json
      printf "  \"never_run\": [" > json
      for (i = 1; i <= nnr; i++) printf "%s\"%s\"", (i > 1 ? ", " : ""), nrv[i] > json
      print "]" > json
      print "}" > json

      printf "eval-trend: %d cases · %d failing · %d drift suspects · %d stale coverage · %d never-run\n", nc, sum_fail, sum_susp, sum_stale, nnr
      printf "eval-trend: wrote %s and %s\n", md, json
    }
  ' "$tsv"
}

# ---------------------------------------------------------------- report

generate_report() {
  local tmp rc
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-trend.XXXXXX")
  collect_rows "$tmp/rows.tsv"
  if [ "$rows_kept" -eq 0 ]; then
    echo "eval-trend: no usable rows under $TREND_RESULTS" >&2
    rm -rf "$tmp"
    return 1
  fi
  aggregate_rows "$tmp/rows.tsv"
  rc=$?
  rm -rf "$tmp"
  return $rc
}

# ---------------------------------------------------------------- selftest

# selftest — fake results/cases dirs drive every flag path; TREND_NOW pinned
# so idle/gap math is deterministic. A failed assertion prints wanted/got.
selftest() {
  local tmp fails=0 c
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/eval-trend-selftest.XXXXXX")
  local res="$tmp/results" cas="$tmp/cases"
  mkdir -p "$res" "$cas/alpha" "$cas/beta" "$cas/gamma" "$cas/delta" \
           "$cas/epsilon" "$cas/zeta-unused"
  for c in alpha beta gamma delta epsilon zeta-unused; do
    echo '{}' > "$cas/$c/expect.json"
  done

  # alpha: 6 steady passes, idle 12 d  → clean, no flags
  {
    echo '{"case":"alpha","pass":true,"ran_at":"2026-06-01T10:00:00Z"}'
    echo '{"case":"alpha","pass":true,"ran_at":"2026-08-01T10:00:00Z"}'
    echo '{"case":"alpha","pass":true,"ran_at":"2026-09-01T10:00:00Z"}'
    echo '{"case":"alpha","pass":true,"ran_at":"2026-10-01T10:00:00Z"}'
    echo '{"case":"alpha","pass":true,"ran_at":"2026-11-01T10:00:00Z"}'
    echo '{"case":"alpha","pass":true,"ran_at":"2026-11-01T11:00:00Z"}'
  } > "$res/2026-06-01-alpha-suite.jsonl"

  # beta: 3 pass then 2 fail  → 3/5, streak 2, suspect:streak
  {
    echo '{"case":"beta","pass":true,"ran_at":"2026-11-10T10:00:00Z"}'
    echo '{"case":"beta","pass":true,"ran_at":"2026-11-11T10:00:00Z"}'
    echo '{"case":"beta","pass":true,"ran_at":"2026-11-11T11:00:00Z"}'
    echo '{"case":"beta","pass":false,"ran_at":"2026-11-12T10:00:00Z"}'
    echo '{"case":"beta","pass":false,"ran_at":"2026-11-13T09:00:00Z"}'
    echo '{"pass":true}'   # no case → skipped row
  } > "$res/2026-11-10-beta-suite.jsonl"

  # gamma: pass 2026-08-01, fail 2026-11-13  → suspect:gap-uncovered (104 d)
  {
    echo '{"case":"gamma","pass":true,"ran_at":"2026-08-01T10:00:00Z"}'
    echo '{"case":"gamma","pass":false,"ran_at":"2026-11-13T10:00:00Z"}'
  } > "$res/2026-08-01-gamma-suite.jsonl"

  # delta: pretty-printed object (old file format), no ran_at →
  # filename-date fallback 2026-11-12; 1 run, failing → suspect:no-baseline
  cat > "$res/2026-11-12-delta-pretty.jsonl" <<'EOF2'
{
  "case": "delta",
  "pass": false,
  "metrics": {"turns": 3}
}
EOF2

  # epsilon: passing but idle since 2026-08-01  → stale-coverage only
  echo '{"case":"epsilon","pass":true,"ran_at":"2026-08-01T10:00:00Z"}' \
    > "$res/2026-08-01-epsilon-suite.jsonl"

  # empty file + unparseable file → tolerated with a warning
  : > "$res/2026-01-01-empty.jsonl"
  printf '{"case":"x","pass":' > "$res/2026-01-02-broken.jsonl"

  TREND_RESULTS=$res TREND_CASES=$cas
  TREND_OUT_MD=$tmp/trend.md TREND_OUT_JSON=$tmp/trend.json
  TREND_NOW=2026-11-13T12:00:00Z
  generate_report > "$tmp/stdout.txt" 2> "$tmp/stderr.txt"
  local rc=$?
  local md="$tmp/trend.md" json="$tmp/trend.json"

  expect_rc() { # want got desc
    if [ "$1" != "$2" ]; then echo "FAIL $3: want rc=$1 got rc=$2"; fails=$((fails+1));
    else echo "ok   $3"; fi
  }
  expect_grep() { # file pattern desc
    if ! grep -q -- "$2" "$1"; then
      echo "FAIL $3: pattern not in $1: $2"; fails=$((fails+1))
    else echo "ok   $3"; fi
  }
  expect_no() { # file pattern desc
    if grep -q -- "$2" "$1"; then
      echo "FAIL $3: unexpected pattern in $1: $2"; fails=$((fails+1))
    else echo "ok   $3"; fi
  }

  expect_rc 0 $rc "report exits 0"
  expect_grep "$md" '| alpha *| 6 | 5/5 | 0 | - | 2026-11-01 | 12 | - |' "alpha clean 5/5 idle 12"
  expect_grep "$md" '| beta *| 5 | 3/5 | 2 | 2026-11-12 | 2026-11-13 | 0 | failing, suspect:no-baseline, suspect:streak |' "beta 3/5 streak 2 (new case → no-baseline too)"
  expect_grep "$md" 'gamma.*suspect:gap-uncovered' "gamma gap-uncovered"
  expect_grep "$md" 'delta.*suspect:no-baseline' "delta no-baseline (pretty, no ran_at)"
  expect_grep "$md" '2026-11-12-delta-pretty' "delta source file named in suspect bullet"
  expect_grep "$md" '| delta.*| 2026-11-12 | 2026-11-12 |' "delta fallback date in table"
  expect_grep "$md" 'epsilon.*stale-coverage' "epsilon stale coverage"
  expect_grep "$md" 'zeta-unused' "never-run case listed"
  expect_grep "$md" 'summary: 5 cases · 3 failing · 3 drift suspects · 1 stale coverage · 1 never-run' "summary counts"
  expect_grep "$md" 'rows, 1 skipped, 1 warned' "skipped/warned counts"
  expect_grep "$md" 'UPPER BOUND' "gap semantics footnote"
  expect_grep "$json" '"streak_failures": 2' "json beta streak"
  expect_grep "$json" '"last_n_rate": 0.60' "json beta rate"
  expect_grep "$json" '"gap_days": 104' "json gamma gap 104"
  expect_grep "$json" '"first_fail": "2026-11-12T10:00:00Z"' "json beta first_fail"
  expect_grep "$json" '"first_fail": null' "json alpha first_fail null"
  expect_grep "$json" '"never_run": \["zeta-unused"\]' "json never_run"
  expect_grep "$json" '"suspect:gap-uncovered"' "json flags present"
  expect_no  "$json" '"case": "x"' "broken-file case never counted"
  expect_grep "$tmp/stderr.txt" 'WARN unparseable 2026-01-02-broken' "warn line for broken file"

  if [ $fails -eq 0 ]; then
    echo "eval-trend selftest: PASS"
    rm -rf "$tmp"
    return 0
  fi
  echo "eval-trend selftest: FAIL ($fails)"
  echo "artifacts kept at: $tmp"
  return 1
}

# ---------------------------------------------------------------- main

case ${1:-} in
  --selftest) selftest ;;
  -h|--help)  sed -n '2,44p' "$0" | sed 's/^# \{0,1\}//' ;;
  "")         generate_report ;;
  *)          echo "eval-trend: unknown arg '$1' (want --selftest)" >&2; exit 2 ;;
esac
