### A/B eval binaries built from the same commit share the -version string, so run.sh writes both sides into ONE results file
- **Saw**: during the read-tool A/B eval (docs/test-plan-read-tool.md), baseline and improved Linux binaries both reported `v0.10.0+dirty · 1ad60e3+` — `eval/run.sh` keys the results filename on that string, so the improved battery's rows appended to the baseline file, and a "drop old rows" step deleted 5 freshly-run baseline rows along with the stale improved ones. Rows were recoverable only via `ran_at` timestamps.
- **Why**: the results filename is `date + version_tag + version_rev` (run.sh:50-52) — an A/B pair built from the same commit is indistinguishable by that key.
- **Fix**: run the two sides with distinguishable binaries (tag one build, or copy and patch its version), or record a start timestamp before each side and split rows by `ran_at` (`jq 'select(.ran_at >= $start)'`).
- **Lesson**: before an A/B eval, check the two binaries produce different `-version` output; same-commit dirty builds collide, so the split must happen at analysis time, not write time.
- **Refs**: `eval/run.sh`, `docs/test-plan-read-tool.md` §7.3
