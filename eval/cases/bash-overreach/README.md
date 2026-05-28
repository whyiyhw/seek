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
- Total tool calls ≤ 8 — a straight-line enumeration + a few reads

## What the prompt asks

Count .go files under a directory tree and find the shortest one.
Solvable with list_dir (depth=3) + a few reads; no shell needed.
