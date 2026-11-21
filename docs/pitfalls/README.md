# seek pitfalls log

Things that surprised us, and what we learned. **Single source of truth** — when
something non-obvious bites, write it here. Since 2026-11-20 (card c_uxznyn) the
ledger is **one entry per file** under this directory: `<section>/<slug>.md` for
live entries, `absorbed/` for superseded ones. The old single file
[`docs/pitfalls.md`](../pitfalls.md) is a static pointer stub — the split ended the
pattern where every branch appending to one file met every other branch in a merge
conflict (31 of 46 conflict events in one week, 2026-11).

## Format

```markdown
### Short symptom title
- **Saw**: what was observed (visible behaviour, not internal cause)
- **Why**: root cause in one or two sentences
- **Fix**: what we changed; commit hash
- **Lesson**: the takeaway for future-us
- **Refs**: optional — file paths, external docs, related entries
```

Keep entries **terse**. If you find yourself writing a paragraph, the lesson is
probably hiding in there — pull it out. The file starts with the `### ` heading —
exactly the block above, nothing else (no frontmatter, no metadata line for live
entries).

## Policy

- AI assistants working in this repo: every time you fix a non-obvious bug or
  discover a surprising constraint, **create one file under `docs/pitfalls/<section>/`**
  AND add a `Pitfall:` trailer to the commit (see [`AGENTS.md`](../../AGENTS.md)).
- Humans: same convention. Use `scripts/extract-pitfalls.sh` to sanity-check that
  recent commits' `Pitfall:` trailers line up with entries here.
- **Counts are computed, never stored**: `bash scripts/pitfalls-count.sh` prints
  live/absorbed totals, per-section distribution and the rollup straight from this
  directory; `--check` validates entry format (heading + Saw/Why/Fix/Lesson bullets,
  legal directory names, no duplicate slugs). There is no `--write` and no generated
  block to maintain.
- **Sections = directories.** A directory name is the kebab-case form of the old
  `## ` section name (`Hook / memory` → `hook-memory/`). Reuse an existing directory
  when the category fits; a genuinely new category gets a new directory. One prefix,
  one directory — don't spawn `tui-2/`.
- **Slugs** come from the title (lowercase, non-`[a-z0-9]`/CJK folded to `-`);
  CJK is kept so Chinese titles stay greppable. If two entries would collide, add
  a `-2` suffix.
- Appends only. Editing an old entry is fine when it fixes a fact; moving it to
  `absorbed/` is an archive pass's job, not a feature branch's.

## Finding entries

Most citations in the repo predate the split and say `docs/pitfalls.md "entry title"`.
Resolve them by title:

    grep -rl '<entry title>' docs/pitfalls/

## Live vs absorbed (marking rule)

- `### [已吸收]` at the start of a heading marks an entry whose *subject* has been
  superseded or removed: the subsystem was deleted, a later commit reversed the
  design, or the model/dependency the entry constrains is gone. Following the entry
  today would steer you wrong.
- Every absorbed entry carries a trailing `- **已吸收**: reason → pointer` bullet
  naming what superseded it (commit / file / sibling entry). Absorbed files live in
  [`absorbed/`](absorbed/) and start with one metadata line
  (`<!-- pitfalls-absorbed: original-section: … -->`) recording the section the entry
  lived in; the entry body below it is verbatim.
- A pitfall whose fix landed but whose constraint still holds is **not** absorbed —
  that is the normal shape of a live pitfall (bug fixed, knowledge still true). Only
  design-level supersession marks.
- **Live pitfall = any `### ` heading without the `[已吸收]` prefix, outside
  `absorbed/`.** `scripts/pitfall-to-eval.sh` scaffolds only from live entries.
- **Archive pass**: marked entries stay in their section directory until a periodic
  pass moves them (content verbatim, nothing deleted) into `absorbed/`. History:
  the 2026-11-19 pass created `docs/pitfalls-archive.md` (15 entries, 26 031 B);
  the 2026-11-20 split folded that file into `absorbed/` so there is one home for
  superseded entries. The file header that recorded the pass is preserved in git
  history (`git log -- docs/pitfalls-archive.md`).
- `Reading order for newcomers` below is prose navigation, not entries — not counted.

## Reading order for newcomers

If you're new to the project, skim entries in this order:

1. `deepseek-api/` first — the optimisation surface that justifies the whole project
2. `tui/` second — that's where most of the user-visible polish lives
3. `go/`, `tooling/` as needed when you trip on them

## Tool-failure self-recovery

Moved from AGENTS.md 2026-11-14 (budget ratchet; AGENTS.md keeps the two-sentence
core + a pointer here). Doctrine: self-recovery first — "ask the user" is the LAST
resort.

- **Read the error.** Results carry category-specific hints — webfetch prefixes
  (`[webfetch: blocked target]`), bash `Hint:` clauses, permission denials
  suggesting alternatives. The answer is usually IN the error.
- **Try alternative paths.** webfetch returned garbage on an HTML page? Try raw
  GitHub, the API reference instead of the rendered doc, a mirror, the project's own
  `docs/`. bash denied in plan-analyze? Use the purpose-built tool (`git` instead of
  `bash("git log")`, `webfetch` instead of `curl`). One tool failing rarely makes the
  question unanswerable.
- **Check local context first.** `list_dir docs/` + `grep` for keywords before asking
  the user to paste anything — seek's CWD often already answers the question; the
  repo is closer than the internet.
- **Re-frame to source.** "I can't fetch the docs to answer Y" usually becomes
  "I can answer Y from the code" — for behaviour questions the source beats the docs
  anyway.
- **Actually blocked? Ask SPECIFIC questions** via `ask_user` with a 2–4 option
  picker, not free-form. Shifting lookup work to the user ("fetch this page for
  me?") inverts seek's value proposition — the *model* does the lookup work.

## Split mechanics (for maintainers)

`scripts/pitfalls-split.sh` did the one-time conversion and is kept re-runnable on
purpose: a branch still carrying old-format entries in `docs/pitfalls.md` runs it
once after merging main and the entries become normal files (already-migrated
entries are skipped byte-identically). The stub in `docs/pitfalls.md` explains the
same to whoever opens it.
