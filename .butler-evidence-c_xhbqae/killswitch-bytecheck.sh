#!/bin/bash
# Byte-shape proof for the kill-switch: the same real run.sh + real judge.py,
# same deterministic fake binary, three case variants of read-tail-fact.
#   v1: real expect.json (judge.mode=gate) + SEEK_EVAL_JUDGE=0   <- kill-switch
#   v2: real expect.json with the judge block deleted            <- pre-J1 shape
#   v3: real expect.json with judge.mode=record, judging enabled <- record era
set -u
repo=$1
ev=$2
tmp=$(mktemp -d "${TMPDIR:-/tmp}/gate-byte.XXXXXX")
sb="$tmp/sb"
mkdir -p "$sb/eval/tools" "$sb/eval/cases/read-tail-fact"
cp "$repo/eval/run.sh" "$sb/eval/run.sh"
cp "$repo/eval/tools/judge.py" "$sb/eval/tools/judge.py"
cp "$repo/eval/cases/read-tail-fact/prompt.txt" "$sb/eval/cases/read-tail-fact/prompt.txt"
fake="$ev/fake-seek"

# v1 — real expect.json (gate), kill-switch on
cp "$repo/eval/cases/read-tail-fact/expect.json" "$sb/eval/cases/read-tail-fact/expect.json"
SEEK_EVAL_JUDGE=0 SEEK_EVAL_RESULTS_DIR="$tmp/r1" bash "$sb/eval/run.sh" read-tail-fact "$fake" >"$tmp/v1.out" 2>&1
echo "v1 rc=$?" > "$tmp/rcs"

# v2 — judge block deleted (pre-J1 shape), judging enabled
jq 'del(.judge)' "$repo/eval/cases/read-tail-fact/expect.json" > "$sb/eval/cases/read-tail-fact/expect.json"
SEEK_EVAL_RESULTS_DIR="$tmp/r2" bash "$sb/eval/run.sh" read-tail-fact "$fake" >"$tmp/v2.out" 2>&1
echo "v2 rc=$?" >> "$tmp/rcs"

# v3 — record mode (record era), judging enabled
jq '.judge.mode = "record"' "$repo/eval/cases/read-tail-fact/expect.json" > "$sb/eval/cases/read-tail-fact/expect.json"
SEEK_EVAL_RESULTS_DIR="$tmp/r3" bash "$sb/eval/run.sh" read-tail-fact "$fake" >"$tmp/v3.out" 2>&1
echo "v3 rc=$?" >> "$tmp/rcs"

# Normalise away the two genuinely time-varying fields, then compare bytes.
for v in 1 2 3; do
  jq -c 'del(.ran_at,.elapsed_s)' "$tmp/r$v"/*.jsonl > "$tmp/n$v.json"
done
echo "--- v1 vs v2 (kill-switch vs no-judge-block) ---"
if cmp -s "$tmp/n1.json" "$tmp/n2.json"; then echo "IDENTICAL (byte-for-byte after del(.ran_at,.elapsed_s))"; else
  echo "DIFF:"; diff <(jq -S . "$tmp/n1.json") <(jq -S . "$tmp/n2.json"); fi
echo "--- v1 vs v3 (kill-switch vs record era) ---"
diff <(jq -S 'keys' "$tmp/n1.json") <(jq -S 'keys' "$tmp/n3.json") && echo "same key set (no judge keys either way)" || true
echo "--- rows ---"
echo "v1 (kill-switch): $(cat "$tmp/n1.json")"
echo "v3 (record):      $(cat "$tmp/n3.json")"
echo "--- console ---"
cat "$tmp/v1.out" "$tmp/v2.out" "$tmp/v3.out" | grep -E '^(PASS|FAIL|RECORD|eval: [0-9])' || true
cat "$tmp/rcs"
rm -rf "$tmp"
