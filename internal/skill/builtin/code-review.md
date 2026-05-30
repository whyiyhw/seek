---
name: code-review
description: Review the current diff for correctness bugs and reuse/simplification/efficiency cleanups at the given effort level (quick: terse, only high-confidence blocking bugs; thorough: exhaustive, incl. uncertain and long-tail design findings). Pass --comment to post findings as inline PR comments, or --fix to apply the findings to the working tree after the review. Triggered by /code-review (and its alias /review). Skip for general "explain this code" questions — this is a structured review pass, not a walkthrough.
---

# code-review

Triggered by `/code-review [quick|thorough] [--fix] [--comment] [branch]` (and its shorthand
`/review`, which is `/code-review quick`). The slash command injects the chosen effort + flags
and the diff to review into your prompt — read them there, then follow the methodology below.
Defaults: `quick`, no `--fix`, no `--comment`, working-tree diff.

## What you review

The diff in scope (working-tree changes, or a branch diff if a branch was passed). Look for:

- **Correctness** — bugs, nil/None derefs, off-by-one, mistaken error paths, race conditions.
- **Security** — injection, unvalidated input, secret leakage, SSRF, auth gaps.
- **Design** — wrong abstraction, leaky boundaries, missing tests for failure modes.
- **Cleanups** — reuse over reinvention, simplification, dead code, efficiency.

NOT your job: lint/format nits a linter already catches (tell the user to run `pre-commit` or
their IDE linter), or rewriting working code to taste. Focus on what an LLM finds that static
tools don't.

## Effort levels (precision ↔ recall)

Two levels. The injected prompt names one — match its framing:

- **quick** (default) — precision-first. Report ONLY the blocking bugs you are highly confident
  are real. Suppress speculation and style nits. A handful of the most important issues, terse.
  An empty result is a valid answer.
- **thorough** — recall-first / exhaustive. Broad sweep across correctness / security / perf /
  design, PLUS edge cases, concurrency, error paths, and test gaps. You MAY include uncertain
  findings — label each with a confidence (high/medium/low) and say what would confirm it — and
  surface long-tail design/maintainability issues, not just blocking bugs. Noisy by design.

> Why two and not four: a 2026-05 eval (`eval/cases/code-review-effort-*`) found DeepSeek
> doesn't reliably separate finer prompt-framing gradations — run-to-run variance swamped the
> signal. Legacy `low`/`medium` still map to `quick`, `high`/`max` to `thorough`.

## Output shape

Group findings by severity (Critical / High / Medium / Low / Nit). For each: `file:line`, a
one-line description, why it matters, and — for `high`/`max` — a confidence tag. End with a
one-line verdict. If the diff is clean at this effort level, say so plainly; do not invent
findings to fill space.

## --fix (apply findings to the working tree)

Only when the prompt says `--fix` is set. Otherwise review is read-only — do NOT write files.

1. Finish the read-only review first and present the findings.
2. Turn the mechanically-fixable findings into a `propose(problem, steps)` call — each step a
   concrete, verifiable fix ("Fix nil-deref at handler.go:42", "Extract duplicated parse into a
   helper"). Skip findings that need a human judgement call (design rewrites, behaviour changes);
   list those for the user instead of proposing them.
3. On approval you enter plan-execute. Apply fixes step by step. Each edit still prompts y/N
   (or per-step if the user chose auto-approve-per-step). Stay within approved scope — re-propose
   if a fix turns out larger than stated. This is the standard plan-mode loop; read the
   `plan-mode` skill if you need the full state machine.

## --comment (post inline PR comments)

Only when the prompt says `--comment` is set. Requires the `gh` CLI, authenticated.

1. Pre-check first: `bash command -v gh` (and `bash gh auth status`). If `gh` is missing or
   unauthenticated, do NOT fail the review — print the full findings in chat and tell the user
   to install gh and run `gh auth login` to post inline comments, then stop.
2. If `gh` is ready, post via `gh pr review --comment` (a single review whose body summarises
   the findings; use the review API for line-level comments). Confirm in chat what you posted.

## Anti-goals

- No cloud "ultra" multi-agent review — seek is a local tool; that mode is out of scope.
- No custom lint rulesets — that is `pre-commit` / IDE-linter territory.
- Never auto-write without `--fix`, and never bypass the propose / per-call gate even with `--fix`.
