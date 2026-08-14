# git-subcommand-midtask

## What this probes

The same duplicated-subcommand tic as `git-subcommand-shape`
(`{"subcommand":"diff","args":["diff","--stat"]}`), but in the context
it actually appeared in: MID-TASK, right after the model made an edit
and is verifying its own work. The directed variant (fresh session,
"盘点仓库" instruction) went 10/10 with 0 dupes — the tic did not
reproduce there — supporting the transcription-error theory: the model
thinks `git diff --stat` as a shell command while absorbed in a task
and transcribes it wholesale into the tool call, whereas a directed
prompt makes it think in tool-shape from the start.

## Setup/teardown

setup.sh creates an untracked `git-shape-scratch/notes.txt` (outside
eval/tmp — that dir is gitignored; the file must be visible to git
status). teardown.sh removes it. The model reads + edits the file with
the purpose-built tools, then verifies with `git diff --stat` and
`git status --short`.

## Expected behaviour

- ≥ 2 git calls (diff + status verification)
- 0 duplicated-subcommand notes
- edit goes through the edit tool (bash tolerance is 2, not 0 — the
  metric under test is the git call shape, and tool-choice noise should
  not mask it)

## Caveat

If this variant ALSO shows 0 dupes across a meaningful sample, the tic
is likely long-context dependent (the motivating session was deep in a
conversation; eval runs are fresh single-prompt sessions) and the
harness cannot reproduce it — at which point the auto-fix, not the
description, is the mitigation that matters, and this case remains as
a regression guard.
