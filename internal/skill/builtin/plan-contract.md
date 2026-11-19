---
name: plan-contract
description: Use BEFORE calling propose for any non-trivial task — 3+ files, a new subsystem, a migration, or anything irreversible. Defines what makes a seek plan closable (completion conditions as checkable assertions, not prose), where a gate belongs (irreversible step, or output nobody reads item by item), how to route each decision to 检 script / 查 different-source subagent / 定 human, and how a finished loop becomes a dated pending entry instead of another permanent rule. Pairs with plan-mode — that one is the propose/plan state machine, this one is the plan's content.
---

# plan-contract — what a seek plan must contain to be worth approving

`plan-mode` tells you *how* to drive the propose/plan tools. This skill tells you *what goes in*
the plan, *who checks it*, and *when it counts as done*. A plan that violates the rules here is
approvable but not closable — the user will approve it and you will both be unable to say whether
it finished.

## 1. Vocabulary (one name per mechanism — do not drift)

| Term | In seek |
|---|---|
| **message** | the originating ask: a user turn, a Linear/ONES ticket, a failing eval case |
| **plan** | the `propose(problem, done_when, steps, why_now, reviewed_by)` payload, snapshotted to `~/.seek/projects/<id>/plans/` |
| **plan review** | checking the plan *before* execution, from a source that did not write it |
| **execute** | the `plan-execute` substate: writes/bashes unlocked |
| **result review** | checking the output after execution, from a source that did not produce it |
| **delivery** | merged, ticket closed, *and site cleaned* (worktrees, bg jobs, sessions) |
| **retrospective** | comparing delivered vs planned; output is a rule, a gate, or a deletion |
| **gate** | a check that refuses to let work move on. In seek: `plan-analyze` (no writes) and the `propose` picker |
| **rule** | a written practice with no enforcement. In seek: AGENTS.md, a skill body, a comment |
| **different source** | a different session; ideally a different model or a human |

A gate blocks. A rule only reminds. The most common retrospective output is upgrading a rule that
got skipped into a gate.

## 2. Where the gate belongs

Two halves of one judgement:

- **A step that cannot be undone needs a gate before it.**
- **An output nobody reads item by item needs a gate before it moves on.**

Apply it to seek honestly:

| Action | Reversible? | Gate |
|---|---|---|
| edit / write inside the worktree | yes (git) | none — plan-execute's per-call prompt is enough |
| `git commit` on a local branch | yes | none |
| **merge to main** | the revert is; the *deploy* is not | result review + human |
| **`seek serve` / release publish** | no | human, before |
| **deleting worktrees / sessions** | no | human, before (list exact ids first) |
| **`push --force` to a shared branch** | no | human, before |
| **writing production data** | no | human reads the plan, not just the diff |

Two gates already exist and cost nothing: `plan-analyze` cannot write, and `propose` blocks until
the user answers. `propose` approves **the plan**, not each action — say so if the user seems to
think otherwise.

Do not add gates freely. A gate's cost is a human waiting. The judgement rule above tells you
*where* a gate belongs; whether to pay for it is a retrospective decision (§8), not a planning one.

## 3. What a plan must contain

The propose schema has five fields. The plan needs five parts; here is where each rides:

| Part | Field |
|---|---|
| header (links, blocking plan, date, status) | `why_now` for the blocker; status lives in the transcript, not the plan |
| background + decisions (**including where you changed your mind**) | `problem` |
| **completion conditions — numbered assertions** | **`done_when` (a real field, and required)** |
| step breakdown | `steps` |
| **who checks the result** | `reviewed_by` (recommended whenever a step is irreversible or nobody reads its output) |

`done_when` is not a convention any more — the tool rejects a plan without it, with an error
that names the field and tells you to go back to analysis. That makes it a **gate** in the §1
sense, not a rule: you cannot propose prose and call it a plan. The tool enforces presence,
count (1–12, 3–5 recommended), and length (≤ 240 chars each); it cannot enforce that an
assertion is *checkable*. That part is on you, and §4 is the vocabulary for it.

Worked example, within the real limits (steps ≤ 200 chars, ≤ 20 items; done_when ≤ 12):

```
propose(
  problem="Session files are JSONL but `Load()` is called when only line-1
metadata is needed, so `-resume` pays a full parse per listing.

Confirmed: keeping one file per session (not splitting meta into a sidecar) — a sidecar
reintroduces the parallel-state drift the transcript is supposed to prevent.",
  done_when=[
    "loadMeta() reads only line 1 — test with a 10k-message fixture asserts no full parse",
    "seek -list on 50 sessions completes without calling Load() (grep the call path)",
    "go test -race ./internal/session/... green; schema_version still 2",
    "AGENTS.md's session-format bullet names loadMeta as the metadata-only path",
  ],
  steps=[
    "Add loadMeta() to internal/session reading only the first line → check: 10k-fixture test",
    "Route printSessionList through loadMeta → check: -list output byte-identical on the corpus",
    "Update the AGENTS.md session-format bullet → check: grep finds loadMeta in AGENTS.md",
  ],
  reviewed_by="new agent session given plan + diff + this comment; human decides what to fix",
)
```

If you cannot fill `done_when` with **checkable** lines, the task is not yet plannable. Go back to
ANALYZE or say so — the tool will refuse the call anyway, and you should not try to satisfy it with
"it works" lines to get past the gate.

## 4. Completion conditions are relative judgements, not "correct"

There is no assertion that proves a feature is correct (the oracle problem). Do not try to write
one. Decompose instead. The seven classes that actually work, with seek's own instances:

1. **Doc sync** — AGENTS.md / `docs/prd/<feature>.md` / `docs/guide-<topic>.md` / CHANGELOG names it; a `- **Refs**:` line lands in pitfalls.
2. **Regression conservation** — `go test -race ./...` green; `eval/run.sh <case>` does not regress; `scripts/weight.sh` raises no new SLIM advice.
3. **Consistency / invariant** — CLI and TUI hit the same dispatcher; `pkg/deepseek` still does not import `pkg/llm`; `TestCompose_IsDeterministic` still passes; a tool schema constant is byte-identical across calls.
4. **Trigger rules** — the flag / env var / slash command actually fires. This is the class that needs an E2E: build the binary, run it, assert the exit code (`tools_flag_test.go` is the model).
5. **State** — the artifact file exists at its documented path; a pending-entry ledger has the row; `-resume` replays to the same place.
6. **Shape / boundary** — dependency direction holds; the only writer under `~/.seek/skills/` is `internal/skillmgr`; the schema exposes no parameter that bypasses an output cap.
7. **Field test** — the real binary, the real TUI, the real API. Weak and slow, but it catches the class assertions cannot: legal syntax, wrong layout.

Every one of these is weak on its own. That is the point — each is separately checkable, and
together they cover most of what "done" means without pretending to solve the oracle problem.

**Fixing a plan is cheaper than fixing code.** Changes land cheapest at the plan, costlier in code
(test + result review re-run), costliest after delivery (triage + repair). When you spot a factual
error while writing steps, correct the plan.

## 5. Route each decision: 检 / 查 / 定

A plan is mostly decisions. Route by *where the answer lives*, not by difficulty:

| Where the answer is | Who answers | In seek |
|---|---|---|
| **shape of the completion condition** — is it an assertion, does it have a red-then-green path | **检** script | `grep`, `go vet`, a test skeleton |
| **business fact** — is this a bug or by design; which config does this fee read | **查** different source | `agent(subagent_type="explore")` in plan-analyze, `webfetch` for upstream docs |
| **tradeoff** — worth fixing? add a lock? | **查** if a written yardstick exists (threat model, default-don't-fix list), else **定** | same |
| **intent** — do it at all, how far, which first | **定** human | `ask_user`, **batched**, never one interruption at a time |

A business fact is an information gap, not a judgement — the answer exists, the agent just cannot
reach it. Every time a doc fills that gap, this class converts from 查 to something the agent
looks up itself.

## 6. Different source is a floor, not a formality

Run the plan review as a **separate session** that did not write the plan — in seek that is the
`agent` tool, not a self-check. Same-model new session is the floor. Two traps:

- **Three agents agreeing is not three pieces of evidence.** Same training, same blind spot. If
  you need real independence, route the reviewer to a different lane (`provider="glm"` /
  `"opencode"`) or pair it with a mechanical check.
- **Feed the reviewer the material, not your conclusion.** Plan + diff + the original message +
  the rules. Do **not** hand it the previous round's finding list unless you also ask it to find
  new problems — anchoring is real, and it narrows the review to the previous review's scope.

Reviewers are wrong too. They produce a problem list; a human decides what to fix. Say that
explicitly when you hand the list over.

## 7. Delivery is the ring that breaks

Delivery = merged **and** closed **and** site cleaned. It breaks for two reasons: it is the tail
(nobody is chasing it once the work "feels done"), and cleanup involves deletion (irreversible, so
it gets postponed forever).

For seek, "site cleaned" means all three, checked explicitly:

- worktree removed (`exit_worktree`), no stray `~/.seek/projects/<id>/worktrees/*`
- background jobs killed (`monitor(action="kill")`), no orphaned subagent rows
- sessions pruned if this loop created throwaway ones

The safe order, and it is a gate rather than a rule because deletion cannot be undone:
**confirmed merged → confirmed ticket closed → list the exact ids to delete → human nods → delete.**
Never delete by prefix, never as a side effect of something else.

## 8. Closing the loop: rules are a liability too

A retrospective produces exactly one of three things, and "nothing" is not one of them:

1. **A rule** — write it down, no enforcement.
2. **A gate** — only if the rule was observed being skipped *and* the skip cost something.
3. **A pending entry** — the honest default when you have a lesson but no evidence.

A pending entry carries three things and nothing else:

```
- **Watch**: the phenomenon (a countable thing, not a vibe)
- **Counts as**: what would make it real
- **Review by**: <date> — then keep / withdraw / defer once
```

**Measurements that fail do not count as passing.** "No data" defaults to withdraw, not to keep.

This section exists because the repo already has the anti-pattern it prevents:
`docs/pitfalls.md` holds 249 live entries and **0 closed** — an integrator that only takes input.
Before adding entry #249, ask whether it is a rule worth its maintenance cost or a pending entry
that expires. `docs/budget.json` + `scripts/weight.sh` is the good precedent in the same repo: a
budget that reports overrun and ratchets down, instead of a list that only grows.

Rules and gates are debts. When maintaining one costs more than the incident it prevents, delete
it. Two readiness numbers tell you whether a rule is alive: how many times it is cited (a rule
nobody references may be dead) and how often the same class of incident recurs after it landed.

## 9. Anti-patterns, observed

- **A plan with no completion conditions.** It is a description, not a source. Reject it.
- **15 steps.** Unapprovable in practice. Split, or propose a first batch that ends with "re-plan."
- **Staying in L2 forever** — every plan manually approved, nobody actually reading them. The system is right most of the time, so review degrades into a glance, and the human still owns the blame. Either go back to reading carefully, or accumulate evidence and move the class upward.
- **Self-expanding scope mid-execute.** Doing "the natural next thing" that was not in the approved steps. Re-propose instead. This includes expanding your own permissions: scope and authority are set outside the conversation, by the user, never granted by a party inside it.
- **Skipping the plan review because the task "seems obvious."** That is the case where the blind spot is most likely — you did not read enough to know it was obvious.
- **Recording a lesson as a rule when you have no evidence it prevents anything.** Make it a pending entry with a date.
- **Counting a silent skip as a pass.** No data is not a green light.

## 10. What this skill does not do

- It does not replace `plan-mode` for the tool mechanics (substates, `plan(start/complete)`, re-propose on adjust) — read that one for state transitions.
- It does not authorise writes outside the approved plan, and it does not arm the per-step pre-approval gate. `propose` approval is required first; per-call y/N still applies unless the user chose auto-approve-per-step.
- It does not force plan mode on exploration. When you cannot state "done when" before starting, plan after the fact and label it an archive — that is a legitimate mode, not a failure.
- It does not make `done_when` *correct*. The tool enforces that the field exists and is shaped like assertions; whether an assertion actually catches the failure it claims to is the judgement this skill is trying to train, and no schema can do that for you.
