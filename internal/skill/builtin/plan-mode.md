---
name: plan-mode
description: Read this when the mode reminder says `plan-analyze` or `plan-execute`, OR the user explicitly types `/plan` mid-task. Explains the analyze → propose → execute → report loop, when to call `propose` vs `ask_user`, and how to handle user disagreement without redoing finished work. Skip when you're in normal (ask) mode — plan mode is opt-in via `/plan` and you'll see the substate in the per-message mode reminder.
---

# Plan mode workflow (analyze → propose → execute → report)

Plan mode is a **gated workflow**, not a verbosity setting. The user enters it with `/plan` when they want to (a) define a problem before any writes, (b) explicitly approve scope, (c) keep you from drifting once execution starts.

You will see one of two **substate reminders** appended to user turns while plan mode is on:

- `[Mode: plan-analyze — ...]` — read-only. You can use `read`, `grep`, `list_dir`, `git`, `think`, `ask_user`. **You cannot** call `write`, `edit`, or `bash`.
- `[Mode: plan-execute — ...]` — the user has approved your plan. Writes are unlocked but each one still prompts for per-call approval; you're expected to execute the approved steps, narrate progress in chat, and stay within scope.

Substate transitions are driven by the `propose` tool, not by `/plan` (which only toggles the mode on/off entirely).

## The loop

### Step 1 — ANALYZE (plan-analyze substate)

Gather context until you can write a self-contained problem statement and a concrete plan:

- `read` / `grep` / `list_dir` the relevant code.
- `git` for history if the task is about a recent change.
- `think` for non-trivial planning — same rules as the dual-model skill (`docs/prd/feature-plan-mode.md` cross-references it).
- `ask_user` **when you have a genuine ambiguity** that you can't resolve from reading (e.g. "you said 'auth refactor' — do you mean the middleware or the token store?"). Use the picker shape — 2–4 options. Do not use `ask_user` for free-form "what do you want?" — those are signs you haven't read enough yet.

When you can answer "what's the problem?" and "what's the plan?" without hedging, you're ready for Step 2.

### Step 2 — PROPOSE (still plan-analyze; gate to execute)

Call `propose(problem, steps, why_now?)`. This pops a picker for the user with three options: **approve**, **adjust** (with optional free-text feedback), **cancel**.

What makes a good proposal:

- **Problem statement**: one paragraph, self-contained. The user reads this in the picker — assume they've forgotten the prior turn.
- **Steps**: 3–8 verifiable actions. Each step is something a human could check ("Add X handler in handlers.go", "Update integration tests for Y"), **not** an internal phase ("think about Z", "consider edge cases"). Don't nest sub-bullets — if a step is too big, refine it.
- **Why now** (optional): use to surface hidden assumptions ("this assumes #234 is merged", "skipping mobile because it's frozen until March 5").

Example translation from a `think` reasoner output to a `propose` call:

> reasoner Answer: "Refactor auth middleware to a per-request token store, including interface design, migration, and tests."

```
propose(
  problem="Auth middleware currently shares a process-wide token store, which makes per-request token rotation impossible. We need to migrate to a per-request store without breaking existing handler call sites.",
  steps=[
    "Inventory current middleware call sites (grep RequireAuth)",
    "Define TokenStore interface in internal/auth/store.go",
    "Migrate session-backed store, keeping API compatible",
    "Wire new middleware in cmd/server/main.go",
    "Update integration tests"
  ],
  why_now="This depends on the session cookie refactor (#234) which merged yesterday."
)
```

**Do NOT** use `propose` for:
- Clarifying ambiguity before you have a plan — that's `ask_user`'s job.
- Status reports mid-execution — narrate in chat.
- Picking between N pre-defined alternatives — that's `ask_user`'s job (and `propose` returns a 3-option picker, not your custom set).

### Step 3 — EXECUTE (plan-execute substate)

After approval, you're in `plan-execute`:

- Work the steps **in order**. Each step is a discrete narration unit — say what you're about to do, do it, say what happened.
- Each `write` / `edit` / `bash` call still pops a y/N prompt for the user. That's by design; the user retained per-call veto.
- **Stay within approved scope.** If you discover the plan was incomplete (e.g. a step you didn't anticipate is needed), **stop and re-propose** instead of "just doing the extra thing". Scope drift is the failure mode plan mode exists to prevent.

If the user signals disagreement mid-execution (e.g. "wait, that's not right" or "let's not touch X"), you've effectively been adjusted — see Step 4.

### Step 4 — ADJUST loop (back to plan-analyze)

User disagreement at any point comes through one of two paths:

1. **Inside the propose picker** they pick "adjust" (often with free-text feedback). The `propose` tool's result text will tell you.
2. **Mid-execute** they push back in chat. You should infer this and re-propose.

When this happens:

1. **First, summarize what's already done in chat.** Plain English: "I've completed steps 1 and 2 (interface defined, migration done). Step 3 (wiring) is in progress — I've changed handlers.go but not main.go yet." This **lives in the transcript** so your next plan can build on it without redoing finished work.
2. **Then revise the plan** with the user's feedback as the primary constraint, and **call `propose` again**.
3. **Do not silently restart** without summarizing — the user has no way to know what was already done.

### Step 5 — REPORT

When all approved steps are done, say so explicitly:

> Completed: [recap]. Anything else?

This is the "exit ramp" — without it the user has to scroll to figure out whether you're done or stuck. After report, the user typically either accepts ("looks good, /plan off") or starts a new request (which re-enters analyze).

## Cancellation

If the user picks **cancel** in the propose picker, `/plan` is toggled off entirely. You're back in normal mode; the conversation continues without the plan workflow. Don't try to keep planning — they explicitly said stop.

## Common pitfalls

- **Proposing too early**: skipping ANALYZE because the task "seems obvious". User then adjusts every proposal because you missed context. Fix: don't call `propose` until you've actually read the relevant files.
- **Proposing too many steps**: 15-step plans are a planning-level smell. If you have that many steps, the user can't usefully approve them. Refine to ≤ 8, or split into a first-batch proposal that ends with "and then we re-plan based on what we learned".
- **Drifting in execute**: doing one thing in step 3 that "felt natural" but wasn't approved. Re-propose; don't go around the gate.
- **Restarting silently after adjust**: the user adjusts, you immediately re-propose with no chat summary of what's done. The new plan duplicates work. Fix: **always summarize first**.

## Cost discipline

`propose` and `ask_user` both block waiting for user input. Each one is a real interruption — use them sparingly. Two `ask_user` calls during ANALYZE and one `propose` to gate is typical. Five `ask_user` calls + three `propose` cycles in one task means you're being either too cautious or insufficiently prepared in ANALYZE.

## What this skill does NOT do

- It does not authorise running destructive commands without per-call approval. Even in `plan-execute`, every `bash` / `write` / `edit` prompts for y/N — `propose` approves the plan, not each individual action.
- It does not replace `ask_user` for ambiguity resolution. `propose` is for "commit to a plan"; `ask_user` is for "answer a question".
- It does not run when you're outside plan mode. If the user wants you to plan rigorously without entering plan mode, they should toggle `/plan` first.
