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

- Zero `bash` calls *before* a dedicated tool — bash as first resort is the
  overreach being measured (`max_bash_before_dedicated: 0`)
- One `bash` call *after* a dedicated call is tolerated as recovery
  (`max_bash_calls: 1`, recalibrated 2026-11-18 — see Drift history)
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
- **Root-caused (2026-11-18, card c_lrilqc)** — the post-repair full
  lane record is **red-green ALTERNATING, not regressed**: 4 PASS /
  7 FAIL, with both outcomes on the SAME binary (2d16948: F F P P ·
  b914a22: P F F · 26dce15: F P · 56ec735 ablation full arm: F).
  Prompt + `list_dir`/`bash` descriptions are byte-stable since the
  11-13 repair (`f99707f`) — no drift; and the case never had a green
  baseline at all (born 2026-05-28 with EMPTY result stubs, first
  measured 11-13), so "failing since 11-13" is really "coin flip
  since first measurement". Verdict: **fragile case at a stochastic
  margin**, aggravated by a structural trap — `list_dir` defaults
  `depth=1`, so the natural first call on `internal/tools/` lists the
  24 package dirs and answers NEITHER question, and nothing in the
  output pointed at the `depth` re-call; the model then coin-flips
  between `list_dir(depth=2)` and a `bash find | wc` recovery. Every
  post-repair FAIL row has `list_dir ≤ 1, bash ≥ 1`; every PASS row
  is exactly `list_dir=1, bash=0` — that arg is the whole
  discriminator. Fix (code, this card): default-depth listings that
  contain subdirectories now end with an in-band expansion hint
  naming the `depth=2` re-call (truncation-notice idiom,
  `internal/tools/listdir/listdir.go`) — the recovery path is finally
  signposted in-band instead of only in the description. Expected
  trend: green; confirm with the ≥5-round re-run (card acceptance ③,
  deferred post-merge pending the real-key scope decision on
  c_lmrp73).
- **Recalibrated (2026-11-18, ≥5-round re-run)** — over 5 fresh rounds on `11b2366`
  the shape split: 2 rounds re-called `list_dir(depth=2/3)` and stopped, 3 fell
  back to `bash find | wc` *after* `list_dir`. The old `max_bash_calls: 0` was
  green 2/5 — yet all 5 put `list_dir` FIRST (`bash_before_dedicated` = 0,
  replayed from the saved streams). The bound conflated "needed a shell" with
  "reached for a shell". Same remedy the suite already applied to
  `tool-selection` (pitfalls: "bound what the mechanism forces, not the
  aspiration"): `max_bash_calls 0 → 1` (single recovery envelope) + new
  `max_bash_before_dedicated: 0`, with that metric now counting `list_dir` as a
  dedicated tool. Replaying the 5 saved streams against the new gate: **5/5
  pass**. A 6th live run minutes later opened with `bash` twice and never
  called `list_dir` (`bash_before_dedicated: 2`, `list_dir_calls: 0`) — a TRUE
  overreach, and still red, so the gate kept its teeth. Net: the case now fails
  only on overreach (observed ~1-in-6 at this prompt); raw `bash_calls` stays
  recorded as the trend signal.
