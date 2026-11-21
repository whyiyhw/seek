# seek pitfalls log — moved to docs/pitfalls/

One entry per file under [`docs/pitfalls/`](pitfalls/): `<section>/<slug>.md` for live
entries, [`pitfalls/absorbed/`](pitfalls/absorbed/) for superseded ones. The format
template and the policy live in [`docs/pitfalls/README.md`](pitfalls/README.md).

Finding an entry cited elsewhere (most citations say `docs/pitfalls.md "entry title"`):

    grep -rl '<entry title>' docs/pitfalls/

Counts are computed live, never stored: `bash scripts/pitfalls-count.sh` prints them,
`--check` validates entry format. Do NOT append entries to this file — create a new
file under `docs/pitfalls/` instead. A branch that still carries old-format entries
here can run `bash scripts/pitfalls-split.sh` once to convert them.
