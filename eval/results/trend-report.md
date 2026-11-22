# eval trend report
generated: 2026-11-22T08:18:12Z   source: /Users/whyiyhw/code/github/seek/scripts/../eval/results (25 files, 393 rows, 5 skipped, 0 warned)
window: last 5 runs · gap threshold: 30 days

summary: 35 cases · 3 failing · 3 drift suspects · 0 stale coverage · 1 never-run

| case                         | runs | last-5 | streak | first fail | last run | idle d | flags |
| :--                          | ---: | ---: | ---: | --- | --- | ---: | --- |
| redundant-tool-calls         | 12 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| instruction-following        | 11 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| tool-args-hallucination      | 10 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| tool-selection               | 14 | 5/5 | 0 | 2026-11-13 | 2026-11-19 | 3 | - |
| code-review-effort-low       | 12 | 5/5 | 0 | 2026-05-29 | 2026-11-19 | 3 | - |
| code-review-effort-max       | 14 | 1/5 | 0 | 2026-11-13 | 2026-11-19 | 3 | - |
| read-tail-fact               | 19 | 5/5 | 0 | 2026-08-14 | 2026-11-19 | 3 | - |
| read-eof-blind               | 19 | 5/5 | 0 | 2026-08-14 | 2026-11-19 | 3 | - |
| read-whole-file              | 19 | 5/5 | 0 | 2026-08-14 | 2026-11-19 | 3 | - |
| write-guard-fallback         | 19 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| git-subcommand-shape         | 25 | 5/5 | 0 | 2026-08-18 | 2026-11-19 | 3 | - |
| git-subcommand-midtask       | 23 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| git-multi-query-no-bash      | 31 | 5/5 | 0 | 2026-08-18 | 2026-11-19 | 3 | - |
| edit-unicode-read-first      | 10 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| write-elided-edit-fallback   | 10 | 5/5 | 0 | 2026-11-16 | 2026-11-19 | 3 | - |
| grep-refine-not-flood        | 11 | 3/5 | 0 | 2026-11-16 | 2026-11-19 | 3 | - |
| yolo-allows                  | 10 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| print-deny-semantics         | 10 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| session-id-contract          | 10 | 5/5 | 0 | - | 2026-11-19 | 3 | - |
| bash-overreach               | 15 | 1/5 | 2 | 2026-11-13 | 2026-11-19 | 3 | failing, suspect:streak |
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
| serve-web-stream             | 5 | 5/5 | 0 | - | 2026-11-21 | 1 | - |
| grep-affordance              | 3 | 0/3 | 3 | 2026-11-22 | 2026-11-22 | 0 | failing, suspect:no-baseline, suspect:streak |
| parallel-fanout              | 3 | 3/3 | 0 | - | 2026-11-22 | 0 | - |
| references-trigger           | 3 | 3/3 | 0 | - | 2026-11-22 | 0 | - |

## drift suspects / failing
- **bash-overreach** — `failing` `suspect:streak`: 2 consecutive failures — sustained, not a one-off (latest run: 2026-11-19 from `2026-11-19-v0.11.1-0.20261119035710-11b2366d61a3-11b2366.jsonl`)
- **mcp-semble-routing-a3-nl** — `failing` `suspect:no-baseline` `suspect:streak`: first recorded run 2026-11-18 is within 30 d of now and already failing — no historical baseline; 5 consecutive failures — sustained, not a one-off (latest run: 2026-11-18 from `2026-11-18-v0.11.1-0.20261118172804-bc1fd08f7137-bc1fd08.jsonl`)
- **grep-affordance** — `failing` `suspect:no-baseline` `suspect:streak`: first recorded run 2026-11-22 is within 30 d of now and already failing — no historical baseline; 3 consecutive failures — sustained, not a one-off (latest run: 2026-11-22 from `2026-11-22-v0.12.2-0.20261122063357-b7c1bb525a57-b7c1bb5.jsonl`)

## never-run cases (case dir exists, zero recorded runs)
- **subagent-bg-resume** — no baseline at all; first run may surprise (bash-overreach shape)

---
flags: failing=latest run failed · suspect:gap-uncovered=fail after >30d uncovered · suspect:no-baseline=≤5 runs, first run <30d old, failing · suspect:streak=≥2 consecutive fails · stale-coverage=idle >30d
note: a failure date after a coverage gap is only an UPPER BOUND on when the behaviour drifted. Rows are jq-parsed (old files are pretty-printed concatenated objects, not one-per-line JSONL); rows missing ran_at fall back to the filename UTC date.
