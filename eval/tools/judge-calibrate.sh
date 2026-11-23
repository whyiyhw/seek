#!/bin/bash
# judge-calibrate.sh — eval v3 J2: run the judge over the calibration
# fixtures and produce the agreement report (the J-line's legitimacy
# source: 判分必须可复算 → the LABELS and the REPORT are the recomputable
# artifacts; the verdicts are samples).
#
#   eval/tools/judge-calibrate.sh [fixtures.jsonl]
#
# Env (same discipline as eval/run.sh's judge path — v8.md §9.1: the
# default lane is the OpenCode Go subscription; the metered key only via
# an explicit human SEEK_EVAL_PROVIDER=deepseek):
#   SEEK_EVAL_BINARY        seek binary to drive (default ./seek)
#   SEEK_EVAL_JUDGE_MODEL   judge model (default kimi-k3.1-pro)
#   SEEK_EVAL_PROVIDER / SEEK_EVAL_BASE_URL / SEEK_EVAL_API_KEY
#                           provider lane knobs (default opencode)
#   SEEK_EVAL_JUDGE=0       skip everything (kill-switch parity)
#
# Outputs (eval/judge-calibration/, both checked in by hand after a
# human review — the report is the calibration record):
#   verdicts.jsonl   one row per fixture: id/case/origin/label/verdict/
#                    reason/agree
#   report.md        agreement, confusion matrix, and the DIRECTION split
#                    (false-pass = label fail, verdict pass — the danger
#                    direction; gate eligibility: agreement >= 0.85 AND
#                    false-pass == 0)
#
# Exit 0 whenever a full pass was measured (verdicts themselves may be
# doubt-heavy — that is data); exit 2 on missing fixtures/binary.
# macOS bash 3.2 compatible.
set -u
cd "$(dirname "$0")/../../" || exit 2

command -v jq >/dev/null 2>&1 || { echo "judge-calibrate: jq required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "judge-calibrate: python3 required" >&2; exit 2; }

fixtures=${1:-eval/judge-calibration/fixtures.jsonl}
[ -f "$fixtures" ] || { echo "judge-calibrate: fixtures not found: $fixtures" >&2; exit 2; }

if [ "${SEEK_EVAL_JUDGE-1}" = "0" ]; then
  echo "judge-calibrate: SEEK_EVAL_JUDGE=0 — judging disabled, nothing to measure" >&2
  exit 0
fi

binary=${SEEK_EVAL_BINARY:-./seek}
[ -x "$binary" ] || [ -f "$binary" ] || { echo "judge-calibrate: seek binary not found/executable: $binary (build with: go build -o seek ./cmd/seek)" >&2; exit 2; }

judge_model=${SEEK_EVAL_JUDGE_MODEL:-kimi-k3.1-pro}
seek_eval_provider=${SEEK_EVAL_PROVIDER:-opencode}
seek_eval_base_url=${SEEK_EVAL_BASE_URL-}
seek_eval_api_key=${SEEK_EVAL_API_KEY-}

# Same lane-args shape run.sh builds for the judge (eval/run.sh "Judge
# lane args"): provider + base-url, never the SUT model.
judge_lane_args=""
if [ "$seek_eval_provider" != deepseek ]; then
  judge_lane_args="-provider $seek_eval_provider"
fi
if [ -n "$seek_eval_base_url" ]; then
  judge_lane_args="$judge_lane_args -base-url $seek_eval_base_url"
fi
if [ -n "$seek_eval_api_key" ]; then
  case "$seek_eval_provider" in
    deepseek) export DEEPSEEK_API_KEY="$seek_eval_api_key" ;;
    opencode) export OPENCODE_API_KEY="$seek_eval_api_key" ;;
    *)        export OPENAI_API_KEY="$seek_eval_api_key" ;;
  esac
fi
if [ "$seek_eval_provider" = opencode ] && [ -n "$seek_eval_base_url" ]; then
  export OPENCODE_BASE_URL="$seek_eval_base_url"
fi
if [ "$seek_eval_provider" != deepseek ]; then
  unset DEEPSEEK_API_KEY || true
fi

outdir=$(dirname "$fixtures")
work=$(mktemp -d "${TMPDIR:-/tmp}/judge-calibrate.XXXXXX")
trap 'rm -rf "$work"' EXIT

verdicts="$outdir/verdicts.jsonl"
: > "$verdicts"
total=$(wc -l < "$fixtures" | tr -d ' ')
n=0
while IFS= read -r line; do
  [ -n "$line" ] || continue
  n=$((n+1))
  id=$(jq -r '.id' <<<"$line")
  case_name=$(jq -r '.case' <<<"$line")
  label=$(jq -r '.label' <<<"$line")
  origin=$(jq -r '.origin' <<<"$line")
  printf '%s\n' "$line" | jq '{rubric, prompt, final_text}' > "$work/in.json"
  if ! python3 eval/tools/judge.py \
      --seek-binary "$binary" \
      --lane-args "$judge_lane_args" \
      --model "$judge_model" \
      --input "$work/in.json" \
      --out "$work/out.json" >/dev/null 2>&1; then
    verdict=doubt
    reason="judge error: judge.py exited nonzero"
  else
    verdict=$(jq -r '.verdict // "doubt"' "$work/out.json" 2>/dev/null)
    reason=$(jq -r '.reason // ""' "$work/out.json" 2>/dev/null)
  fi
  [ "$verdict" = pass ] || [ "$verdict" = fail ] || [ "$verdict" = doubt ] || verdict=doubt
  agree=$([ "$verdict" = "$label" ] && echo true || echo false)
  jq -cn --argjson id "$id" --arg case "$case_name" --arg origin "$origin" \
    --arg label "$label" --arg verdict "$verdict" --arg reason "$reason" \
    --argjson agree "$agree" --arg model "$judge_model" \
    '{id:$id, case:$case, origin:$origin, label:$label, verdict:$verdict, reason:($reason|.[0:200]), agree:$agree, model:$model}' >> "$verdicts"
  printf '  [%d/%d] %-34s label=%-5s verdict=%-5s %s\n' "$n" "$total" "$case_name" "$label" "$verdict" "$agree" >&2
done < "$fixtures"

# ---- aggregate -----------------------------------------------------------
agg=$(jq -s '
  def cnt(f): map(select(f)) | length;
  {
    n: length,
    agree: cnt(.agree),
    false_pass: cnt(.label=="fail" and .verdict=="pass"),
    false_fail: cnt(.label=="pass" and .verdict=="fail"),
    label_pass_verdict_doubt: cnt(.label=="pass" and .verdict=="doubt"),
    label_fail_verdict_doubt: cnt(.label=="fail" and .verdict=="doubt"),
    label_doubt_verdict_pass: cnt(.label=="doubt" and .verdict=="pass"),
    label_doubt_verdict_fail: cnt(.label=="doubt" and .verdict=="fail"),
    confusion: (group_by([.label, .verdict]) | map({pair: (.[0] | {label, verdict}), n: length}))
  }' "$verdicts")
agreement=$(jq -r 'if .n > 0 then (.agree / .n * 100 | round / 100) else 0 end' <<<"$agg")
false_pass=$(jq -r '.false_pass' <<<"$agg")
eligible=$([ "$(jq -r '.n > 0 and (.agree / .n) >= 0.85 and .false_pass == 0' <<<"$agg")" = true ] && echo yes || echo no)
# Lane-outage suspect (first stability night, 2026-11-23: a 4/34 run was
# 32× "judge error: seek exited 1" — the OpenCode request-window quota
# closing mid-run, the glm-429 family on the opencode lane). A run whose
# doubt mass is mostly judge ERRORS measures lane availability, not judge
# quality; mark it so nobody reads 0.12 as a judge regression.
judge_err_doubts=$(grep -c 'judge error' "$verdicts" || true)
outage_suspect=no
if [ "$judge_err_doubts" -gt $(( $(jq -r '.n' <<<"$agg") / 2 )) ]; then
  outage_suspect=yes
  eligible="no (lane-outage suspect — ${judge_err_doubts} judge-error doubts)"
fi

{
  echo "# Judge calibration report"
  echo
  echo "- ran_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "- fixtures: \`$fixtures\` ($(jq -r '.n' <<<"$agg") rows: $(jq -s 'map(select(.origin=="real"))|length' "$verdicts") real / $(jq -s 'map(select(.origin=="constructed"))|length' "$verdicts") constructed)"
  echo "- judge model: \`$judge_model\` on the \`$seek_eval_provider\` lane"
  echo "- **agreement: ${agreement}** ($(jq -r '.agree' <<<"$agg")/$(jq -r '.n' <<<"$agg"))"
  echo "- **false-pass (label fail → verdict pass): $false_pass** — the danger direction"
  echo "- lane-outage suspect (judge-error doubts > half the rows): $outage_suspect"
  echo "- false-fail (label pass → verdict fail): $(jq -r '.false_fail' <<<"$agg")"
  echo "- label pass → doubt: $(jq -r '.label_pass_verdict_doubt' <<<"$agg"); label fail → doubt: $(jq -r '.label_fail_verdict_doubt' <<<"$agg")"
  echo "- label doubt → pass/fail (overreach): $(jq -r '.label_doubt_verdict_pass + .label_doubt_verdict_fail' <<<"$agg")"
  echo "- **gate eligibility (agreement ≥ 0.85 AND false-pass = 0): $eligible**"
  echo
  echo "## Confusion (label → verdict)"
  echo
  echo '```'
  jq -r '.confusion[] | "\(.pair.label) -> \(.pair.verdict): \(.n)"' <<<"$agg"
  echo '```'
  echo
  echo "## Disagreements"
  echo
  echo '```'
  jq -r 'select(.agree == false) | "[\(.id)] \(.case) (\(.origin)) label=\(.label) verdict=\(.verdict): \(.reason)"' "$verdicts"
  echo '```'
} > "$outdir/report.md"

echo "judge-calibrate: $(jq -r '.agree' <<<"$agg")/$(jq -r '.n' <<<"$agg") agree (${agreement}), false-pass ${false_pass}, gate-eligible ${eligible}"
echo "  verdicts: $verdicts"
echo "  report:   $outdir/report.md"
