# write-elided-edit-fallback

**Source pitfall**: docs/pitfalls.md "Filesystem / model safety" — *"A read that reached EOF but got elided still vouched for the whole file"* (line 1273). A full read whose output exceeded the 64 KiB result cap recorded an elided note instead of an observation, so the write guard refuses with `StatusElided` — whose message names `edit` as the recovery, explicitly NOT "read it again" (the same parts elide every time; re-reading can never clear the note). Scaffolded by `scripts/pitfall-to-eval.sh --make 1273`, hand-finished as one of the first three seeded cases.

## What this probes

Whether the model works WITH the elision contract instead of against it,
on a file where whole-file `write` is structurally impossible:

- the fixture is ~74 KiB / 100 lines — above the 32 KiB whole-read
  threshold, but within the 200-line default limit, so one `read`
  reaches EOF with the middle elided (head 8 KiB + tail 24 KiB kept).
  No sequence of reads can ever mark it observed (only an un-elided
  `offset=1` full read observes), so `write` is refused every time
- the healthy path is `grep` to locate the line → windowed
  `read(offset, limit)` → one exact `edit`. The best model never
  attempts `write` at all
- tolerated-but-suboptimal: one `write` attempt → refused (`write
  refused: …`) → model switches to `edit`. `max_write_refusals: 1`
  allows exactly this; a second refusal means the refusal text was not
  read
- pathology: re-reading the whole file hoping the note clears
  (`max_read_calls: 3` allows full read + window + one spare, not a
  loop)

Sibling of `write-guard-fallback` (partial windowed read → refused →
edit); the distinct axis here is the ADVICE-following: StatusElided's
recovery text names `edit`, and the case pins that the model takes the
named exit instead of the impossible one.

## Expected behaviour

- ≥1 `edit` call (`min_edit_calls: 1`) — the only possible mutation path
- ≤1 `write` refusal (`max_write_refusals: 1`); zero is the ideal
- ≤3 `read` calls, ≤2 `grep` calls — locate + window + land, no loop
- `bash`-sed is counted as a non-target shape (it bypasses the guard
  the case exercises, and the prompt's "byte-identical" demand is what
  `edit` exists for)

## What the prompt asks

Change `retention_days: 30` to `retention_days: 14` in
`eval/tmp/write-elided-edit-fallback/config-dump.txt`, keeping every
other byte identical.

- `setup.sh` generates the fixture deterministically: 100 lines of
  ~740 bytes; line 55 carries the retention knob and sits ~40 KiB in,
  inside the elided middle (8–50 KiB) — the model cannot see it on a
  full read and must narrow first
- `teardown.sh` records the offline gold: `retention14=1 retention30=0
  lines=100`
