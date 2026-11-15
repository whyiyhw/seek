# eval trend report
generated: 2026-11-13T15:45:47Z   source: /Users/whyiyhw/.paseo/worktrees/2znanydc/drive-c-lxferp/scripts/../eval/results (13 files, 131 rows, 0 skipped, 0 warned)
window: last 5 runs · gap threshold: 30 days

summary: 20 cases · 2 failing · 2 drift suspects · 0 stale coverage · 0 never-run

| case                       | runs | last-5 | streak | first fail | last run | idle d | flags |
| :--                        | ---: | ---: | ---: | --- | --- | ---: | --- |
| redundant-tool-calls       | 3 | 3/3 | 0 | - | 2026-11-13 | 0 | - |
| instruction-following      | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| tool-args-hallucination    | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| tool-selection             | 4 | 2/4 | 2 | 2026-11-13 | 2026-11-13 | 0 | failing, suspect:streak |
| code-review-effort-low     | 4 | 3/4 | 0 | 2026-05-29 | 2026-11-13 | 0 | - |
| code-review-effort-max     | 4 | 4/4 | 0 | - | 2026-11-13 | 0 | - |
| read-tail-fact             | 11 | 5/5 | 0 | 2026-08-14 | 2026-11-13 | 0 | - |
| read-eof-blind             | 11 | 5/5 | 0 | 2026-08-14 | 2026-11-13 | 0 | - |
| read-whole-file            | 11 | 5/5 | 0 | 2026-08-14 | 2026-11-13 | 0 | - |
| write-guard-fallback       | 11 | 5/5 | 0 | - | 2026-11-13 | 0 | - |
| git-subcommand-shape       | 17 | 5/5 | 0 | 2026-08-18 | 2026-11-13 | 0 | - |
| git-subcommand-midtask     | 15 | 5/5 | 0 | - | 2026-11-13 | 0 | - |
| git-multi-query-no-bash    | 22 | 5/5 | 0 | 2026-08-18 | 2026-11-13 | 0 | - |
| edit-unicode-read-first    | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| write-elided-edit-fallback | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| grep-refine-not-flood      | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| yolo-allows                | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| print-deny-semantics       | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| session-id-contract        | 2 | 2/2 | 0 | - | 2026-11-13 | 0 | - |
| bash-overreach             | 2 | 0/2 | 2 | 2026-11-13 | 2026-11-13 | 0 | failing, suspect:no-baseline, suspect:streak |

## drift suspects / failing
- **tool-selection** — `failing` `suspect:streak`: 2 consecutive failures — sustained, not a one-off (latest run: 2026-11-13 from `2026-11-13-v0.10.4-0.20261113110124-7a4544a9fe94_dirty-7a4544a_.jsonl`)
- **bash-overreach** — `failing` `suspect:no-baseline` `suspect:streak`: first recorded run 2026-11-13 is within 30 d of now and already failing — no historical baseline; 2 consecutive failures — sustained, not a one-off (latest run: 2026-11-13 from `2026-11-13-v0.10.4-0.20261113110124-7a4544a9fe94_dirty-7a4544a_.jsonl`)

---
flags: failing=latest run failed · suspect:gap-uncovered=fail after >30d uncovered · suspect:no-baseline=≤5 runs, first run <30d old, failing · suspect:streak=≥2 consecutive fails · stale-coverage=idle >30d
note: a failure date after a coverage gap is only an UPPER BOUND on when the behaviour drifted. Rows are jq-parsed (old files are pretty-printed concatenated objects, not one-per-line JSONL); rows missing ran_at fall back to the filename UTC date.
