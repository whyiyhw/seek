### Committed metric snapshots must not embed absolute home paths
- **Saw**: `skipped_paths` in the committed baseline JSON carried `/Users/<name>/.seek/sessions/...` — the username and home layout enter public git history through a diagnostics field nobody reads.
- **Why**: the analyser's `skipped_paths` is a debugging affordance (which files were unreadable), and the cheapest implementation is "the path you already have" — but the JSON's destination is a repo, not a terminal. A field's *purpose* and its *audience* diverge after it's committed.
- **Fix**: `transcript-stats` emits `os.path.basename(p)` in `skipped_paths`; `--selftest` asserts the entry equals the basename and contains no path separator.
- **Lesson**: when a diagnostics field is written into a committed artifact, store the **minimum identity** the reader needs (filename), and get full paths from a local re-run. Add the assertion at write time — a privacy leak is invisible in review diffs of a 30 KB JSON.
- **Refs**: `scripts/transcript-stats` (`analyse`, `_selftest`), `eval/transcript-baseline/2026-11-19.json`
