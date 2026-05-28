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
with `grep` + `read`. A model that has NOT internalised the hints
will reach for `git show`, `bash cat`, or edit without reading first.

This is the primary A/B metric for the three description-string
changes. Run before and after the edits, then compare `git_calls`
and `bash_calls`.

## Expected behaviour

- Zero `git` calls — grep+read is cheaper and won't truncate
- Zero `bash` calls — dedicated tools exist for every step
- At least 1 `grep` call — to locate the `const description` lines
- At least 3 `read` calls — one per file to capture the exact text

## What the prompt asks

Find and quote verbatim the `const description` strings from three
files (bash, edit, git tools). Compare their word counts. Pure
read-only inspection — no edits needed.
