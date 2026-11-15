# bash-overreach

## What this probes

Whether the model defaults to `bash` for filesystem inspection when
dedicated tools (`list_dir`, `grep`, `read`, `git`) are available and
more efficient. The prompt asks a repo-inspection task solvable with
`list_dir` + `read`; a model that hasn't internalised the tool
descriptions will reach for `bash find ... | wc -l` or `bash ls -R`.

This case directly measures whether the bash tool's description
("Prefer dedicated tools") reduces unnecessary bash calls. It's the
primary A/B metric for the description-hint approach tested in
[bash.go description change].

## Expected behaviour

- Zero `bash` calls — the task is pure repo inspection
- At least 1 `list_dir` call — the fastest way to enumerate files
  (and the only dedicated tool that prints per-entry byte sizes)
- Total tool calls ≤ 8 — a single list_dir suffices; headroom for a
  confirming read

## What the prompt asks

Count .go files under a directory tree and find the byte-smallest
one. Solvable with one list_dir call (it prints each entry's size);
no shell needed.

## Drift history

- **2026-05-28** — created alongside the description-hint A/B loop.
  Its committed result rows were EMPTY (`a3f2ca1` added blank
  `.jsonl` stubs): no archived PASS row for this case exists. The
  "last passed 2026-05-28" on the dispatch card is evidenced only
  for `tool-selection`.
- **2026-11-13** — failing with `bash_calls=2, list_dir_calls=0`.
  Root cause is premise failure, not model verbosity nor description
  regression: the prompt's second question ("which one has the
  fewest lines?") has no dedicated-tool answer — `list_dir` emits
  byte sizes, not line counts, and the tree grew 56 → 84 `.go`
  files, so the line-minimum is only rigorously computable via
  `bash wc`. Fresh transcript: `find … | wc -l` (count — list_dir
  WOULD have covered it: overreach) then
  `find … -exec wc -l | sort -n` (line-min — forced by the question
  itself).
- **Repair (2026-11-13)** — prompt's second question changed from
  "fewest lines" to "smallest in bytes", which the SAME single
  `list_dir` call answers from its per-entry sizes, so
  `max_bash_calls=0` is honest for the whole task again. The count
  question is unchanged. `expect.json` unchanged.
