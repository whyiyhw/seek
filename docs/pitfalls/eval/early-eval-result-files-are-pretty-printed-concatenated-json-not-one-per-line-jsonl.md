### Early eval result files are pretty-printed concatenated JSON, not one-per-line JSONL
- **Saw**: a line-oriented reader over `eval/results/*.jsonl` saw 347 lines but only 131 result rows; the pre-v0.5 files (e.g. the 146-line v0.4.1 file) hold each result as a multi-line pretty-printed object, many objects concatenated.
- **Why**: the current `eval/run.sh` emits one compact object per line, but the files it wrote before ~v0.5 were pretty-printed — the `.jsonl` extension promises a layout the old files don't have.
- **Fix**: `scripts/eval-trend.sh` jq-parses (`jq -r '… | @tsv'`) instead of splitting lines. Two related bites: `@tsv` without `-r` emits a JSON-encoded string (tabs escaped as `\t`) which silently defeats downstream TSV field-splitting; and rows before the `ran_at` field exist need the filename's UTC-date prefix as a fallback timestamp. Commit (card c_lxferp).
- **Lesson**: treat `results/*.jsonl` as a *stream of JSON objects*, never as lines — any new results tooling starts at jq.
- **Refs**: `scripts/eval-trend.sh` (collect_rows), `eval/README.md` "Trend reports"
