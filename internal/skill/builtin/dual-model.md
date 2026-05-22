---
name: dual-model
description: Use BEFORE starting any non-trivial multi-step task — anything touching 3+ files, designing a new module, refactoring across packages, debugging from a vague symptom, or making an irreversible change. Pairs the chat model (you) with the DeepSeek reasoner via the `think` tool, so you get explicit planning up front and self-review at the end. Skip for one-line edits, typo fixes, doc tweaks, or anything the user has already specified to the keystroke.
---

# Dual-model collaboration (think → execute → think)

You are the **chat** model. The `think` tool calls the **reasoner** model — slower, more expensive, no tools, but much stronger at multi-step reasoning. Use it as a planning + review partner, not a default fallback. Two reasoner calls per task is the target; more than three is a smell.

## When this skill applies

Invoke the loop when the user's request matches any of:
- Multi-step task spanning **3+ files** or **3+ logical phases**.
- Designing or refactoring a module — not just editing one.
- Debugging from a vague symptom ("it's slow", "tests flake sometimes") where the cause isn't obvious from one file.
- Irreversible operations (schema changes, destructive shell, anything in shared infra).

Skip the loop for: single-file edits, typos, formatting, doc-only changes, renaming, or tasks where the user already wrote the exact diff they want.

## The loop

### Step 1 — Plan with `think`

Call `think(task=...)` with a **self-contained** problem statement. The reasoner sees no prior conversation; you must paste in the relevant context (filenames, signatures, constraints, what's known to fail).

```
think(
  task="<one-paragraph problem statement, including constraints>",
  context="<relevant code snippets, error messages, prior decisions>"
)
```

The reasoner returns a structured plan ending with `Answer:`. Treat its output as a proposal, not gospel — if a step contradicts the user's stated goal or what the code clearly says, override it and explain why in your next assistant message.

### Step 2 — Execute the plan

Work the plan one step at a time using `read` / `list_dir` / `write` / `edit` / `bash` / `fim_complete`. Don't re-call `think` for each individual step — that defeats the purpose. If you hit something the plan didn't anticipate AND it's load-bearing for the next steps, that's the only time to call `think` mid-execution.

Commit cadence: don't commit unless the user asked. If they did, follow the project's commit conventions exactly.

### Step 3 — Self-review with `think(reflect=true)`

Before reporting "done", do one reflection pass on the actual diff:

```
think(
  reflect=true,
  task="Review the following changes for correctness, missed edge cases, and risks.",
  context="<the diff you produced, plus the original goal>"
)
```

The reasoner returns issues sorted by severity ending with `Answer:`. For each issue:
- **High severity** (correctness bug, breaks an invariant, missing edge case the user named): fix before reporting.
- **Medium**: fix if cheap; otherwise call them out in your final message as known follow-ups.
- **"looks correct"**: report done.

If the reflection surfaces a major gap, you may go back to Step 2 — but only once. A second reflection that surfaces another major gap means the plan in Step 1 was wrong; back up and redo Step 1 with the new information rather than thrashing in Step 2.

## Cost discipline

Each `think` call uses several thousand tokens and is billed at the reasoner rate. Two calls per task is the target; one is fine for small jobs; four or more means you should have re-planned, not re-executed.

If the user passed `--no-think` (or the reasoner is unavailable for the current provider), skip this skill and proceed without it — note in your reply that you did so.

## What this skill does NOT do

- It does not authorise running destructive commands without per-call approval. The `bash`/`write`/`edit` permission gates apply exactly as they would otherwise.
- It does not replace asking the user when the goal is genuinely ambiguous. If you can't write a self-contained Step 1 problem statement, ask the user first.
