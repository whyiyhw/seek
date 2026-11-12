# edit-unicode-read-first

**Source pitfall**: docs/pitfalls.md "Tooling / environment" — *"`edit` tool: small models burn turns retrying byte-mismatched `old_string`"* (line 744). A v4-flash session ran ~10 failed `edit` calls in a row on a Unicode-dense file (`─` `🌙` `○` `▊`) — visually identical strings, byte-different `old_string`. Scaffolded by `scripts/pitfall-to-eval.sh --make 744`, hand-finished as one of the first three seeded cases.

## What this probes

Whether the model reads the exact bytes before editing a Unicode-dense
file, and whether it lands the edit cheaply once the read is done:

1. **read-before-edit** — the edit tool description and AGENTS.md both
   say "read the target lines first to capture exact whitespace". A
   model editing from memory reproduces the original ~10-call burn.
2. **unique old_string under ambiguity** — the fixture has TWO
   identical bare divider rules (24 `─` each). `old_string` = the bare
   rule alone fails with "occurs 2 times"; the model must anchor with a
   neighbouring line. This is the `expected_replacements` discipline.
3. **recovery, not flailing** — the edit tool's closest-candidate diff
   hint (the pitfall's fix) should cap a miss at one retry:
   `max_edit_calls: 3` = initial attempt + one teach + land.

The prompt deliberately does NOT mention the second divider or say
"read first" — the case measures unprompted behaviour.

## Expected behaviour

- ≥1 `read` call (`min_read_calls: 1`) — the read is the whole point
- 1–3 `edit` calls (`min_edit_calls: 1`, `max_edit_calls: 3`); the
  original failure burned ~10
- a whole-file `write` is counted as the non-target shape: the change
  is a surgical one-line swap on a hostile file, which is exactly what
  byte-fidelity via `edit` is for

## What the prompt asks

Swap the bare light-dash rule under the STATUS block for a heavy-dash
rule of the same width in the fixture copy under
`eval/tmp/edit-unicode-read-first/`. Nothing else changes.

- fixture `testdata/panel.txt`: 13 lines, box-drawing borders, CJK
  labels, `● ○ ▊` glyphs, exactly two bare `─`×24 rules (verified
  programmatically at authoring time; teardown re-verifies)
- `setup.sh` copies it into `eval/tmp/` so the model can mutate it
  without dirtying the committed fixture
- `teardown.sh` records `heavy=1 light=1 lines=13` as the offline gold:
  the STATUS rule became `━`×24, the MENU rule stayed `─`×24, and no
  line was added or lost
