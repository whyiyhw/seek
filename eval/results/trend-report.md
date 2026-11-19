# eval trend report
generated: 2026-11-19T05:52:34Z   source: /Users/whyiyhw/code/github/seek/scripts/../eval/results (20 files, 310 rows, 0 skipped, 0 warned)
window: last 5 runs · gap threshold: 30 days

summary: 21 cases · 2 failing · 2 drift suspects · 0 stale coverage · 0 never-run

| case                       | runs | last-5 | streak | first fail | last run | idle d | flags |
| :--                        | ---: | ---: | ---: | --- | --- | ---: | --- |
| redundant-tool-calls       | 12 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| instruction-following      | 11 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| tool-args-hallucination    | 10 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| tool-selection             | 14 | 5/5 | 0 | 2026-11-13 | 2026-11-19 | 0 | - |
| code-review-effort-low     | 12 | 5/5 | 0 | 2026-05-29 | 2026-11-19 | 0 | - |
| code-review-effort-max     | 14 | 1/5 | 0 | 2026-11-13 | 2026-11-19 | 0 | - |
| read-tail-fact             | 19 | 5/5 | 0 | 2026-08-14 | 2026-11-19 | 0 | - |
| read-eof-blind             | 19 | 5/5 | 0 | 2026-08-14 | 2026-11-19 | 0 | - |
| read-whole-file            | 19 | 5/5 | 0 | 2026-08-14 | 2026-11-19 | 0 | - |
| write-guard-fallback       | 19 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| git-subcommand-shape       | 25 | 5/5 | 0 | 2026-08-18 | 2026-11-19 | 0 | - |
| git-subcommand-midtask     | 23 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| git-multi-query-no-bash    | 31 | 5/5 | 0 | 2026-08-18 | 2026-11-19 | 0 | - |
| edit-unicode-read-first    | 10 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| write-elided-edit-fallback | 10 | 5/5 | 0 | 2026-11-16 | 2026-11-19 | 0 | - |
| grep-refine-not-flood      | 11 | 3/5 | 0 | 2026-11-16 | 2026-11-19 | 0 | - |
| yolo-allows                | 10 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| print-deny-semantics       | 10 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| session-id-contract        | 10 | 5/5 | 0 | - | 2026-11-19 | 0 | - |
| bash-overreach             | 15 | 1/5 | 2 | 2026-11-13 | 2026-11-19 | 0 | failing, suspect:streak |
| glm-lane-contract          | 6 | 0/5 | 6 | 2026-11-16 | 2026-11-19 | 0 | failing, suspect:streak |

## drift suspects / failing
- **bash-overreach** — `failing` `suspect:streak`: 2 consecutive failures — sustained, not a one-off (latest run: 2026-11-19 from `2026-11-19-v0.11.1-0.20261119035710-11b2366d61a3-11b2366.jsonl`)
- **glm-lane-contract** — `failing` `suspect:streak`: 6 consecutive failures — sustained, not a one-off (latest run: 2026-11-19 from `2026-11-19-v0.11.1-0.20261119035710-11b2366d61a3-11b2366.jsonl`)

---
flags: failing=latest run failed · suspect:gap-uncovered=fail after >30d uncovered · suspect:no-baseline=≤5 runs, first run <30d old, failing · suspect:streak=≥2 consecutive fails · stale-coverage=idle >30d
note: a failure date after a coverage gap is only an UPPER BOUND on when the behaviour drifted. Rows are jq-parsed (old files are pretty-printed concatenated objects, not one-per-line JSONL); rows missing ran_at fall back to the filename UTC date.
