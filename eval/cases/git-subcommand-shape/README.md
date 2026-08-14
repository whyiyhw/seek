# git-subcommand-shape

## What this probes

Whether the model constructs git tool calls with the subcommand in the
`subcommand` field ONLY, or repeats it as `args[0]` —
`git diff --stat` emitted as `{"subcommand":"diff","args":["diff","--stat"]}`.

## Failing session that motivated this case

Real transcript (2026-08-14, seek session): four consecutive git calls,
two with the duplicated-subcommand shape (`args:["diff","--stat"]`,
`args:["status","--short"]`), each refused by the then-guard and retried
— a wasted round trip per occurrence. The description already contained
"the subcommand itself goes in `subcommand`, never repeated as an arg"
at the time, so prose alone demonstrably did not prevent the tic.

## The measurement instrument

The guard now AUTO-FIXES the shape and prepends a note containing
`repeated from args` to the result (see pitfalls "a refusal the model
does not learn from"). `git_subcommand_dupes` counts those notes: the
mistake rate is directly observable with zero behavioral cost — the
run's final answer is unaffected by the fix, so quality and shape can
be scored from the same stream.

## Expected behaviour

- ≥ 3 git calls (diff --stat, status --short, log --oneline -n 5)
- 0 bash-run git (`max_bash_calls: 0` — the prompt pins the tool)
- 0 duplicated-subcommand notes

## Caveat: base rate

The tic appeared in 2 of 4 calls in the motivating session but may be
per-session. A single run proves little either way; compare the dupes
rate across ≥ 8 runs baseline vs description-tuned, not single PASSes.
