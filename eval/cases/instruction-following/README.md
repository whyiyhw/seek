# instruction-following

## What this probes

Whether the model honours explicit workflow constraints in the
prompt. The prompt forbids `list_dir` and requires `grep` before
any `read`. A model that ignores constraints will list directories
to "get oriented" before realising it shouldn't, or skip the grep
because it "knows" where the symbol is.

This is a leading indicator for prompt-faithfulness regressions —
if the agent's system prompt or skill instructions get more verbose
over time, the model's attention to explicit user constraints often
degrades.

## Expected behaviour

- Zero `list_dir` calls — the prompt explicitly forbids it
- At least 1 `grep` call — the prompt requires it before any read
- The model finds the answer (no constraint on how many `read` calls)

## What the prompt asks

A simple "find where X is defined" task. Answer is obvious from one
grep + one read; the test is whether the model takes the long way
around.
