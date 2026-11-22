# eval trend report
generated: 2026-11-22T12:29:46Z   source: /Users/whyiyhw/code/github/seek/scripts/../eval/results (26 files, 455 rows, 21 skipped, 0 warned)
window: last 5 runs · gap threshold: 30 days

summary: 35 cases · 2 failing · 1 drift suspects · 0 stale coverage · 1 never-run

| case                         | runs | last-5 | streak | first fail | last run | idle d | flags |
| :--                          | ---: | ---: | ---: | --- | --- | ---: | --- |
| redundant-tool-calls         | 14 | 5/5 | 0 | - | 2026-11-22 | 0 | - |
| instruction-following        | 13 | 5/5 | 0 | - | 2026-11-22 | 0 | - |
| tool-args-hallucination      | 12 | 5/5 | 0 | - | 2026-11-22 | 0 | - |
| tool-selection               | 17 | 5/5 | 0 | 2026-11-13 | 2026-11-22 | 0 | - |
| code-review-effort-low       | 12 | 5/5 | 0 | 2026-05-29 | 2026-11-19 | 3 | - |
| code-review-effort-max       | 14 | 1/5 | 0 | 2026-11-13 | 2026-11-19 | 3 | - |
| read-tail-fact               | 21 | 5/5 | 0 | 2026-08-14 | 2026-11-22 | 0 | - |
| read-eof-blind               | 21 | 5/5 | 0 | 2026-08-14 | 2026-11-22 | 0 | - |
| read-whole-file              | 21 | 5/5 | 0 | 2026-08-14 | 2026-11-22 | 0 | - |
| write-guard-fallback         | 19 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| git-subcommand-shape         | 25 | 5/5 | 0 | 2026-08-18 | 2026-11-19 | 3 | - |
| git-subcommand-midtask       | 23 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| git-multi-query-no-bash      | 31 | 5/5 | 0 | 2026-08-18 | 2026-11-19 | 3 | - |
| edit-unicode-read-first      | 10 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| write-elided-edit-fallback   | 10 | 5/5 | 0 | 2026-11-16 | 2026-11-19 | 3 | - |
| grep-refine-not-flood        | 11 | 3/5 | 0 | 2026-11-16 | 2026-11-19 | 3 | - |
| yolo-allows                  | 10 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| print-deny-semantics         | 13 | 5/5 | 0 | - | 2026-11-22 | 0 | - |
| session-id-contract          | 12 | 5/5 | 0 | - | 2026-11-22 | 0 | - |
| bash-overreach               | 18 | 3/5 | 0 | 2026-11-13 | 2026-11-22 | 0 | - |
| glm-lane-contract            | 7 | 1/5 | 0 | 2026-11-16 | 2026-11-19 | 3 | - |
| mcp-semble-routing-a1-nl     | 10 | 5/5 | 0 | - | 2026-11-18 | 4 | - |
| mcp-semble-routing-a1-symbol | 10 | 5/5 | 0 | - | 2026-11-18 | 4 | - |
| mcp-semble-routing-a1-mixed  | 10 | 5/5 | 0 | - | 2026-11-18 | 4 | - |
| mcp-semble-routing-a3-nl     | 5 | 0/5 | 5 | 2026-11-18 | 2026-11-18 | 4 | failing, suspect:no-baseline, suspect:streak |
| mcp-semble-routing-a2-nl     | 5 | 5/5 | 0 | - | 2026-11-18 | 4 | - |
| mcp-semble-routing-a2-symbol | 5 | 5/5 | 0 | - | 2026-11-18 | 4 | - |
| mcp-semble-routing-a2-mixed  | 6 | 4/5 | 0 | 2026-11-18 | 2026-11-18 | 4 | - |
| mcp-semble-routing-a3-symbol | 5 | 5/5 | 0 | - | 2026-11-18 | 4 | - |
| mcp-semble-routing-a3-mixed  | 5 | 5/5 | 0 | - | 2026-11-18 | 4 | - |
| plan-e2e                     | 2 | 2/2 | 0 | - | 2026-11-21 | 1 | - |
| serve-web-stream             | 7 | 5/5 | 0 | - | 2026-11-22 | 0 | - |
| grep-affordance              | 24 | 3/5 | 1 | 2026-11-22 | 2026-11-22 | 0 | failing |
| parallel-fanout              | 3 | 3/3 | 0 | - | 2026-11-22 | 0 | - |
| references-trigger           | 3 | 3/3 | 0 | - | 2026-11-22 | 0 | - |

## drift suspects / failing
- **mcp-semble-routing-a3-nl** — `failing` `suspect:no-baseline` `suspect:streak`: first recorded run 2026-11-18 is within 30 d of now and already failing — no historical baseline; 5 consecutive failures — sustained, not a one-off (latest run: 2026-11-18 from `2026-11-18-v0.11.1-0.20261118172804-bc1fd08f7137-bc1fd08.jsonl`)
- **grep-affordance** — `failing`: failing with recent coverage — check expect.json vs the run (latest run: 2026-11-22 from `2026-11-22-v0.12.4-0.20261122114357-f8117389377d_dirty-f811738_.jsonl`)

## never-run cases (case dir exists, zero recorded runs)
- **subagent-bg-resume** — no baseline at all; first run may surprise (bash-overreach shape)

---
flags: failing=latest run failed · suspect:gap-uncovered=fail after >30d uncovered · suspect:no-baseline=≤5 runs, first run <30d old, failing · suspect:streak=≥2 consecutive fails · stale-coverage=idle >30d
note: a failure date after a coverage gap is only an UPPER BOUND on when the behaviour drifted. Rows are jq-parsed (old files are pretty-printed concatenated objects, not one-per-line JSONL); rows missing ran_at fall back to the filename UTC date.
