# tool-selection

## What this probes

Whether the model selects the right tools for reading source files
when three description-level hints are in effect (or missing, in
baseline):

1. **bash** description: "Prefer dedicated tools (git, grep, read,
   list_dir, webfetch) for repo inspection."
2. **edit** description: "Before calling, read the target lines first
   to capture exact whitespace for old_string."
3. **git** description: "For reading file content, prefer grep+read
   over git show/cat-file — cheaper and won't truncate."

The prompt asks the model to find and quote three `const description`
strings from the codebase. The task is pure file-reading — solvable
with `grep` + `read` up to `read`'s 1024-byte line cap (see
[Drift history](#drift-history) for what happens past it). A model
that has NOT internalised the hints will reach for `git show`,
`bash cat`, or edit without reading first.

This is the primary A/B metric for the three description-string
changes. Run before and after the edits, then compare `git_calls`
and `bash_calls`.

## Expected behaviour

- Zero `git` calls — grep+read is cheaper and won't truncate
- Zero bash calls BEFORE a dedicated tool (`bash_before_dedicated=0`)
  — shell is never the first resort, only an elision-recovery path
- At most 4 `bash` calls total — the forced-recovery envelope:
  `bash.go`'s description line is 1059 B > `read`'s 1024 B line cap
  and > `grep`'s 240-char match truncation (1176 B after the
  2026-11-13 bash-description clause), so the verbatim quote +
  word counts are shell-forced (1 extraction + ≤3 per-file `wc -w`;
  observed range across five real runs: 1–4)
- At least 1 `grep` call — to locate the `const description` lines
- At least 3 `read` calls — one per file to capture the exact text

## Drift history

- **2026-05-28** — created; passed with `bash_calls=0` (rows in
  `results/2026-05-28-baseline-tool-selection.jsonl` and
  `results/2026-05-28-after-description-changes.jsonl`). At that
  commit (4715a6f) the longest target line — `bash.go`'s
  `const description` — was **384 bytes**, well under `read`'s
  per-line cap, so "quote verbatim" was dedicated-tool-solvable.
- **2026-11-13** — failing with `bash_calls` 1–4. Root cause is
  neither model verbosity nor description regression but fixture
  drift: `bash.go:37` grew to **1059 bytes**, crossing `read`'s
  `maxLineBytes = 1024` in-band elision (`internal/tools/read/read.go`;
  `grep` also truncates matched lines at 240 chars,
  `internal/tools/grep/grep.go`). No dedicated tool can return that
  line verbatim any more. Fresh transcripts show the hints still
  working exactly as designed — `grep`×3 → `read`×3, `git=0` — with
  bash used ONLY after `read` returned
  "… [N bytes of this line elided]" (`awk 'NR==36'` extraction, then
  a word-count pass).
- **Repair (2026-11-13)** — `max_bash_calls` 0→4 (the mechanically
  forced recovery envelope above), plus a new
  `max_bash_before_dedicated=0` bound (metric added to `eval/run.sh`)
  so the case still FAILS the actual anti-pattern it was built for:
  bash as a first resort. `max_git_calls=0`, `min_grep_calls=1`,
  `min_read_calls=3` unchanged. Threshold change carries drift
  evidence: 384 B → 1059 B vs the 1024 B cap, verified in-run.

## What the prompt asks

Find and quote verbatim the `const description` strings from three
files (bash, edit, git tools). Compare their word counts. Pure
read-only inspection — no edits needed.
