# grep-refine-not-flood

**Source pitfall**: docs/pitfalls.md "Tooling / environment" — *"Grep tool with broad pattern can deadlock the agent — no recovery via /compact"* (line 670). Two broad greps on a large codebase filled the whole context; the 16 KiB output cap (the pitfall's fix) now bounds each call and its truncation notice tells the model to refine — but the model still has to TAKE that exit instead of enumerating. Scaffolded by `scripts/pitfall-to-eval.sh --make 670`, hand-finished as one of the first three seeded cases.

## What this probes

Whether the model answers an aggregate question over a big log WITHOUT
enumerating it into context:

- the fixture is ~2114 lines × ~260 chars (~550 KiB). The aggregate
  (count + top-3 codes) is not answerable by paging `read` (11 windowed
  reads ≈ the original deadlock) nor by enumerating `grep`: the tool
  caps at `max_matches` 20/200 and — important detail — its "capped"
  notice reports the CAPPED hit count, not the true total, so no
  number of grep calls ever yields the answer
- the healthy exit is instrumentation: one or two scoping greps to
  learn the line shape, then a single bash pipeline
  (`grep … | sort | uniq -c | sort -rn | head`) — pipelines are
  legitimate (`|` is deliberately not counted by `bash_chains`; this
  case does not bound bash at all)
- the pathology is the enumeration loop: re-grepping narrower slices or
  paging reads until the context fills. `max_total_tool_calls: 6` /
  `max_grep_calls: 3` bound only that; they are deliberately loose
  first-pass bounds (eval/README.md: "add bounds as you learn what
  good vs. bad looks like") — tighten with data from real runs

## Expected behaviour

- ≤6 total tool calls, ≤3 of them `grep`, ≤10 turns
- bash pipelines allowed and expected; not bounded here
- offline gold (from the results row's `final_text`): total
  `lvl=ERROR` lines = **614**; top 3 codes = **ERR-03: 110,
  ERR-07: 86, ERR-05: 68**

## What the prompt asks

Report (1) the total number of `lvl=ERROR` lines and (2) the top 3
`code=ERR-xx` values by count, with counts, from the generated log at
`eval/tmp/grep-refine-not-flood/app.log`.

## Fixture arithmetic (setup.sh, deterministic)

- lines 1–2000: every 4th line is `lvl=ERROR` with `code=ERR-(i/4 mod
  10)` → 500 ERROR lines, 50 per code ERR-00…ERR-09; other lines are
  INFO/DEBUG noise with no `error`/`ERR-` tokens
- appended: 60× ERR-03, 36× ERR-07, 18× ERR-05 → totals 110/86/68;
  grand total 614 ERROR lines, 2114 lines overall
- `setup.sh` sanity-checks both totals and fails loudly on drift;
  `teardown.sh` just cleans the scratch dir (the answer is scored from
  `final_text`, not from disk)
