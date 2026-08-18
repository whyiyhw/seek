# git-multi-query-no-bash

## What this probes

Whether the model routes **multiple independent git queries** through the
dedicated git tool, one call per query, when the prompt gives it no
explicit bash ban. The metrics that matter:

- `git_calls ≥ 4` — three `log --all --grep` + one `status --short`,
  one query per call (strict: merging greps into a single call still
  violates one-query-per-call and fails the bound)
- `bash_calls = 0` — no bash at all
- `bash_chains = 0` — no `;`/`&&` command chains
- `bash_git_calls = 0` — no git invoked through bash
- `git_subcommand_dupes = 0` — the duplicated-subcommand tic (see
  `git-subcommand-shape`) stays absent

## Failing session that motivated this case

Real transcript (2026-08-18, seek session on Windows cmd.exe):

1. The model wanted three git history greps and a status. It built ONE
   bash command with POSIX separators:
   `git log --oneline --all --grep=scroll -i; echo ---; git log ...`
   cmd.exe does not treat `;` as a command separator — the whole string
   went to git, which failed with
   `fatal: ambiguous argument 'echo': unknown revision`.
2. The same session called the git TOOL with the subcommand duplicated
   in `args[0]` (`{"subcommand":"show","args":["show","-s",...]}`) —
   the tic `git-subcommand-shape` already covers, kept as a regression
   guard here.

## Design decision: no bash ban in the prompt

The existing `git-subcommand-shape` prompt explicitly says
"不要用 bash 跑 git 命令" — an instruction-following probe. This case
deliberately omits any bash prohibition: the tool descriptions
(`internal/tools/git/git.go`, `internal/tools/bash/bash.go`) must carry
the behaviour. Otherwise the baseline would measure "does the model obey
an explicit ban", not "do the descriptions prevent the tic".

## Caveat: base rate

Like `git-subcommand-shape`, the tic may be per-session and
long-context-dependent; single eval runs are fresh sessions. Compare
rates across ≥ 10 runs baseline vs description-tuned (see
`docs/test-plan-git-tool-shape.md`), not single PASSes.

## Metrics

`bash_chains` and `bash_git_calls` were added to `eval/run.sh`
specifically for this case — see the metric vocabulary in
`eval/README.md`.
