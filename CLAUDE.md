# seek — project guide for AI assistants

This file is read at the start of every Claude Code session in this repo. It mirrors [`AGENTS.md`](AGENTS.md), which **seek** itself auto-loads at startup. The two files are related but allowed to diverge where each agent's tooling differs (see [AGENTS.md vs CLAUDE.md](#agentsmd-vs-claudemd--related-but-not-identical)).

Treat the instructions below as mandatory project conventions.

## Pitfall recording (load-bearing — read this)

When you fix a non-obvious bug, discover an undocumented API constraint, or find a surprising piece of behaviour:

1. **Append an entry to [`docs/pitfalls.md`](docs/pitfalls.md)** using the template at the top of that file. Categorise it (TUI / DeepSeek API / Go / Tooling / etc.); keep it terse — symptom, root cause, fix, lesson, refs.
2. **Add a `Pitfall: <one-line summary>` trailer** to the commit message. Multiple pitfalls in one commit = multiple trailers.
3. The trailer is what `scripts/extract-pitfalls.sh` reads — it's the redundant signal so `docs/pitfalls.md` can be regenerated if it ever falls out of sync.

What counts as "non-obvious":
- A behaviour that surprised you (or the user) for more than a minute.
- An API contract that isn't documented in the obvious spot.
- A workaround for a tooling / classifier / sandbox limitation.
- A design constraint that, if forgotten, would be re-discovered painfully.

What does **not** count:
- Routine typos, missing imports, version bumps, refactors.
- Anything obvious from reading the code or running `go vet`.

If you're not sure: log it. Cheap to add, expensive to recover from memory months later.

## Architecture (non-negotiable)

- **DeepSeek-first, two-tier providers**. `pkg/deepseek` is a first-class client with DeepSeek-specific fields (cache metadata, FIM endpoint, reasoner content). `pkg/llm` is a thin generic interface for second-tier providers (Anthropic / OpenAI / Gemini — landing in M6).
- **`pkg/deepseek` must not import `pkg/llm`** (CI lint enforces this — see `.github/workflows/ci.yml`). The whole point of the split is that DeepSeek-specific optimisations don't get lowered into a generic interface.
- **Skill subsystem (v2)**: read-only loader in `internal/skill` (Anthropic Agent Skills layout: `<dir>/SKILL.md` + frontmatter); install/uninstall/update in `internal/skillmgr`; call-stats JSONL in `internal/skillstats`; shared CLI/TUI dispatcher in `internal/skillcli`. Loader is the only path that runs at startup; everything else is on-demand. **Never** add filesystem writes under `~/.seek/skills/` outside `internal/skillmgr` and `internal/skillstats` — those are the only two packages allowed to mutate user-level skill state.
- **Plan-mode subsystem (v2 + v2.x)**: confirmation-gated workflow `analyze → propose → execute → adjust → report`. Driven by the `propose` tool (`internal/tools/propose/`) and progress-tracked by the `plan` tool (`internal/tools/plan/`). Substate state machine lives in `permission.Policy` (Mode / preApproved flag) + agent mode reminder + TUI status bar; transcript event-sourcing reconstructs state on `seek -resume` (see `plan/reconstruct.go`). Plan-approval artifacts (write-once markdown snapshots) land in `~/.seek/projects/<id>/plans/` via `plan/artifact.go`. Full design + status table in [`docs/prd/feature-plan-mode.md`](docs/prd/feature-plan-mode.md).
- See [`docs/prd/`](docs/prd/) for the full PRD series: v0 initial, v1 Memory, v2 Skill lifecycle, plus standalone feature PRDs (`feature-plan-mode.md`, `feature-webfetch.md`, `feature-permission-refactor.md`, `feature-active-memory.md`, `feature-mcp-client.md`, …).

## Tool usage workflow (load-bearing)

When exploring code, follow this order — skipping steps costs tokens and breaks prefix cache:

1. **grep first** — find exact file + line before reading anything.
2. **read(offset=N) second** — read only the relevant window, not the whole file.
3. `read` accepts an optional `limit` parameter (default 200, max 200 — values above error; small files ≤ 32 KiB are always returned whole). Use `offset=N` to page through larger files.
4. `grep` caps at 20 matches by default (`max_matches` can be raised, but rarely should be).

Never read a whole file to answer a question you could answer with grep. The prefix cache survives only when old messages are byte-identical; lazy whole-file reads balloon prompt tokens and degrade cache hit rate.

5. **read before edit** — before calling `edit`, first `read(offset=N, path=...)` on the target lines to capture the **exact whitespace** of the `old_string`. Do not guess tab depth from memory; the read output preserves it byte-for-byte. A single read call costs less than the error-fix loop from a mismatched `old_string`.
6. **Git queries: one per git-tool call** — never chain, never bash read-only git; several queries = parallel calls. Rationale: `docs/test-plan-git-tool-shape.md`.

## Tool descriptions: the highest-leverage behavioural lever

Tool descriptions are the single most effective place to shape model behaviour: **they are always sent to the API as part of every tool schema**. Unlike project instructions (which are system-prompt territory and may be skimmed or ignored by weaker/faster models), tool descriptions travel with the JSON schema — the model MUST read them to construct a valid tool call.

**Pattern for tuning behaviour** (proven in this repo via `eval/cases/tool-selection/`):

1. **Build an eval case first** — a prompt + expected metric bounds that define the desired behaviour in measurable terms.
2. **Run baseline** — before changing anything, run the eval and record results.
3. **Edit the tool description** — add 10–20 words of targeted guidance. Zero runtime overhead; the string is already being sent.
4. **Run comparison** — re-run the eval and compare the tool-call sequence against baseline. Success = behavioural change (did the model choose the right tool in the right order?), not just binary PASS/FAIL.

**Why this works when project-file entries don't**: model attention favours tokens closest to the user's request. Tool schemas are injected immediately before the generation step; they compete with conversation history for attention, not with the system prompt. A sentence in a tool description is ~10× more likely to influence the next tool call than the same sentence in a project file.

## AGENTS.md vs CLAUDE.md — related but not identical

[`CLAUDE.md`](CLAUDE.md) is read by **Claude Code** at the start of every session in this repo. [`AGENTS.md`](AGENTS.md) is the canonical agent-instruction file — it is auto-loaded by **seek**, and mirrors structural content from here.

The two files share the same pulse but are allowed to diverge where the tooling differs:

- **CLAUDE.md** can leverage Claude Code-specific capabilities (permission model, tool names, workflow patterns) without translating them into seek's vocabulary first.
- **AGENTS.md** may express the same behaviours in seek-specific terms (the eval framework, `grep`+`read` workflow, `internal/tools/` layout, plan-mode FSM).
- **Sync rule**: keep structural content (Architecture, Permission model, Code conventions) identical. Behavioural guidance (Tool usage workflow, Tool descriptions) can differ in phrasing to match the host agent's vocabulary.
- **When editing one, edit the other** — but don't force byte-identical copies. The goal is that both agents arrive at the same behaviour, not that they read the same text.

## Token & prefix-cache constraints (non-negotiable)

DeepSeek charges ~10× less per token on cache hits. The cache key is an **exact byte sequence** of the entire prior message history. This means:

- **Never modify old messages before sending to the API** — even "compression" or "summarisation" of old tool results invalidates the cache and can cost more than sending the full history.
- **Token optimisation happens at write-time** (tool output size limits), not at send-time (in-transit mutation).
- Tool output limits are enforced inside the tool itself. Do not add a post-hoc filter layer in `pkg/agent` or the API path.
- **Anything that must appear in the prompt prefix for correctness but would otherwise vary is captured ONCE per session — never recomputed per turn.** The canonical example is the session date: it's injected as `sysprompt.Header.Date` (computed once at startup in `cmd/seek` and threaded through every `Compose` call plus the subagent Manager's *static* `SessionDate`), NOT via a `time.Now()` inside the assembler — `Compose` must stay a pure function of its `Header` (the `TestCompose_IsDeterministic` guard enforces this). A per-turn date would mutate the prefix on every request and crater the hit ratio. Refresh only at session boundaries (`-resume` = new process = one accepted bust). See `docs/pitfalls.md` "Today's date belongs in the system prompt".

## Permission model

`internal/permission.Policy` is the single gate every dangerous tool action goes through. Read `permission.go` once before changing any gating logic — the design is two orthogonal axes, not a single mode enum.

- **Two axes**:
  - `Preference` (`PrefDeny` / `PrefAsk` / `PrefYolo`) — the user's standing posture toward dangerous actions. "How strict am I." Chosen at startup (`--yolo` flag, `--no-perm`, etc.) or toggled via `/yolo`.
  - `Workflow` (`WorkflowNone` / `WorkflowPlanAnalyze` / `WorkflowPlanExecute`) — the ceremony the user has currently entered. "What am I doing." Toggled via `/plan` and propose-tool events.
- **`Check` runs workflow-first**, then preference. `WorkflowPlanAnalyze` is TERMINAL (its rule set fully decides each Kind, no fall-through). `WorkflowPlanExecute` adds a `preApproved` fast-path for write/edit/bash, then falls through to the preference gate. `WorkflowNone` always falls through.
- **Workflow trumps pref where they conflict**. `PrefYolo + WorkflowPlanAnalyze` is STILL read-only — that's plan mode's raison d'être. The workflow ceremony is a user-chosen safety boundary; pref can't override it.
- **Two state extensions**:
  - `preApproved` (encapsulated in `Policy.exec workflowExecState`) — set by the `plan` tool when the user approved with "auto-approve-per-step" AND a `plan(start=N)` call entered a step. Lets write/edit/bash inside that step auto-pass without `askFn`. **Only meaningful inside `WorkflowPlanExecute`**; other workflows ignore it. Cleared on step `complete`/`skip`, Esc cancellation, `/plan` off, and ANY `SetWorkflow` transition (defense in depth — the field zeros the whole sub-struct on transition so future PlanExecute fields inherit safe-reset).
  - `Action.ReadOnly` — set by the bash tool when the command matches the side-effect-free whitelist (`go vet` / `go list` / `npm ls` / `make -n` / …) AND has zero shell metacharacters. Lets `WorkflowPlanAnalyze`'s `KindBash` branch through. Treat as advisory-from-the-tool, NOT something callers can lie about — `internal/tools/bash/readonly.go` is the only authorised setter.
- **`Action.Display` carries TUI render hints** (Diff for edit, MemoryName/Tagline for memory, SkillName/Source/Target for skill install). `Check` NEVER reads these — they're pass-through data for `askFn` rendering. Adding a new tool with rich y/N text: extend `Display`, not `Action`.
- **Denials are tool results, not fatal errors** (`internal/permission.ErrDenied`). The agent feeds the message verbatim back to the LLM so it can ask the user or reconsider. NEVER bypass this flow — bypassing means the model sees a `ToolExecEnd` failure instead of structured "denied, here's why", and loses the ability to recover the turn. The bash tool further annotates plan-analyze denials with command-specific `Hint:` clauses (cd-prefix / git-via-bash / go-test / metachar fallback) via `internal/tools/bash/hint.go`.
- **Adding a new workflow is expensive**, but cheaper than adding a Kind. New workflows = one new `case` in `Check` + label updates. Before adding a new `Kind`, exhaust the flag approach (`Action.ReadOnly` is the prior art) or push the safety check inside the tool (git tool's subcommand whitelist, webfetch's URL gate are the prior art for "tool owns its gate"). See `docs/prd/feature-permission-refactor.md` for the full design rationale.

## When a tool fails or returns unexpected content

Self-recovery first. "Ask the user to do it" should be the LAST resort, not the first.

- **Read the error message carefully.** Tool results include category-specific hints: webfetch error prefixes (`[webfetch: blocked target]` etc.), bash `Hint:` clauses pointing at specific fixes, permission denials suggesting alternatives. The answer is usually IN the error — re-read before retrying or escalating.
- **Try alternative paths to the same answer.** webfetch returned garbage on an HTML page? Try a different URL form — raw GitHub (`https://raw.githubusercontent.com/...`), the API reference instead of the rendered doc, a mirror, the project's own `docs/` in the source repo. bash denied in plan-analyze? Use the equivalent purpose-built tool (the `git` tool instead of `bash("git log")`, `webfetch` instead of `curl`). One tool failing rarely means the question is unanswerable.
- **Check local context before reaching for external resources.** Before asking the user to paste docs, run `list_dir docs/` and `grep` for keywords — seek's CWD often has README / PRD / comparison docs that already answer the question. The repo is closer than the internet.
- **Re-frame to source code.** "I can't fetch the docs to answer Y" frequently becomes "I can answer Y from the code." For questions about a project's behaviour, the source is more authoritative than its docs anyway.
- **Only when actually blocked, ask SPECIFIC questions** via `ask_user` with a 2–4 option picker, not free-form "what should I do?". The anti-pattern is shifting tool work to the user: "can you fetch this page and paste it for me?" inverts seek's value proposition — users installed seek because they wanted the *model* to do the lookup work, not to become a human glue layer between the model and the web.

## Code conventions

- **Stdlib first**. `pkg/deepseek` has zero external deps and should stay that way. New deps live behind sensible boundaries.
- **Tool JSON schemas are package-level `[]byte` constants**, not built at call time. Identical bytes across turns is what lets DeepSeek's prefix cache hit (`PRD §4.8.1`).
- **Tool schemas must not expose parameters that let the model bypass output size limits.** Examples: `read` has a `limit` field (default 200, max 200 — values above error; small files ≤ 32 KiB are returned whole); `grep` has `max_matches` defaulting to 20; `propose` has `maxItems: 20` on `steps` (`maxSteps` const); the `plan` tool's `index` is `minimum: 1` and runtime-checked against the active plan's step count. If a limit is safety-critical, reject values above the max with a clear error.
- **New tools** live at `internal/tools/<name>/` with a `New(...)` constructor. If the tool can mutate the filesystem or shell, inject a `*permission.Policy`. If the tool wants user choice (picker / approval), inject `*askuser.Policy`.
- **Sink interfaces: don't break the main contract — add OPTIONAL sibling interfaces.** When a new feature needs more context flowing into a tool's Sink, do NOT add parameters to the existing Sink method (every fake / recording sink in tests breaks). Define a sibling interface (`type FooReporter interface { Foo() ... }`) and upcast at the call site: `if r, ok := sink.(FooReporter); ok { r.Foo() }`. The propose tool currently carries four such optional interfaces (`DuplicateChecker`, `ProgressReporter`, `ContextReceiver`, `ArtifactReporter`); none required a Sink signature change. See `docs/pitfalls.md` "Plan artifact write needs context BEFORE Sink.Approved fires" for the rationale.
- **Wire-format strings are contracts when something parses them.** A tool result string like `[plan: approved]` looks like prose, but `internal/tools/plan/reconstruct.go` scans for that prefix verbatim to seed plan-state on resume. Once a marker has a parser, the marker is wire format — additions go AFTER the closing token (`[plan: approved] (auto-approve-per-step)`), never inside it (`[plan: approved batch]` would silently break reconstruction). When introducing a new variant of an already-parsed result, add a test that asserts on `HasPrefix(out, "<marker>")` so future variants can't slip past.
- **Permission denials are tool results, not fatal errors** (`internal/permission.ErrDenied`). The agent feeds the message back to the LLM so it can ask the user — never bypass that flow. See the "Permission model" section above for the full denial contract.
- **Session format is JSONL** (`schema_version=2`, `.jsonl` extension): line 1 is the header (all scalar metadata), lines 2..N are one `deepseek.Message` per line. Legacy `.json` files (schema_version ≤ 1) are migrated on next `Save`. `loadMeta()` reads only line 1 — do not call `Load()` when you only need metadata.
- **Reconstruct state from the transcript — parallel RUNTIME state counts, not just parallel files.** The transcript is the only durable source of truth. Anything derivable from it should be folded from it on demand, never mirrored. Two forms of the same mistake:
  - **A parallel state file.** Resist adding a sibling `.json` / `.md` that mirrors runtime state — you'll fight drift forever. Artifact files (`~/.seek/projects/<id>/plans/`) are the exception: write-once human-readable snapshots, NOT state.
  - **A parallel runtime field.** Holding a fact in a struct *and* reconstructing it on resume gives you two sources that must be kept in step by hand. `permission.Policy`'s `preApproved` is the live example of the interest this charges: because nothing derives it, the code has to remember to zero it on step complete/skip, on Esc cancellation, on `/plan` off, and on *every* `SetWorkflow` transition. Each of those resets is a bug that was available.

  **Default question when a feature needs new state: "can this be folded from the transcript?" — ask it before reaching for "add a field + a reconstruct function".** Plan state is the shape to copy: the `propose` args and `plan(start|complete|skip)` calls in the transcript *are* the state; `reconstruct.go` replays them and nothing mirrors them. Approval is the shape to avoid: runtime `Policy` state plus transcript reconstruction, hybrid, hand-synchronised.

  Smell test: if you're writing a `reconstruct()` for something you also hold in a struct field, you have two sources of truth and the drift bug is scheduled, not hypothetical.

  Why it's a discipline and not a refactor: dsh gets this structurally — every model request is derived from its append-only log (`deriveMessages()`), so state *cannot* drift from the record. seek is not rewriting to that model; the cost/benefit doesn't hold for a single-binary local CLI. Applying the question per feature captures most of the value at none of the cost. See [`docs/dsh-analysis.md`](docs/dsh-analysis.md) §2 and §9.2.

## Testing (load-bearing)

Tests are how seek stays trustworthy. Treat coverage of failure paths as part of the deliverable, not a follow-up.

**The bar: test the failure modes, not just the happy path.** Happy-path-only suites give false confidence and have already let real bugs ship — commit `986a485` was an orphan-tool_calls regression that sat in `pkg/agent` under green CI for weeks because nothing exercised mid-stream cancellation. Every released feature ships with tests for, where applicable:

- **Cancellation** — any ctx-aware function gets a `ctx.Done` test
- **Mid-loop interruption** — streams/event-loops cut at a partial state
- **Malformed input** — LLM-produced JSON, partial SSE, missing required fields
- **Concurrent access** — `-race` is on by default in CI; if a function can be hit concurrently, verify it
- **Persistence round-trip + recovery** — write, reload, AND verify corrupt-state repair (see `session.Repair`)

If one of these doesn't apply, fine. If you skipped a test for one that does, say why in the PR description. "Real-API behaviour only" is valid; "didn't have time" is not.

Coverage is a weak signal but 0% on a function is a strong one. Before claiming a feature done: `go test -cover ./...` and look at anything you touched that's not exercised.

Procedurally:
- `go test ./...` before every commit. CI runs `-race` on three OSes.
- Tests use `httptest` fake DeepSeek backends — no real API key required for the suite.
- For real-API smokes, write the key to `.env` (gitignored) and source it; never put it on the command line.

## Commit messages

- Subject in conventional-commit style: `feat(M3): ...`, `fix(tui): ...`, `chore: ...`.
- Body explains **why**, not what. The diff already shows what.
- `Co-Authored-By: <model name> <noreply@anthropic.com>` trailer on AI-written commits.
- `Pitfall: <summary>` trailer when fixing a non-obvious bug (see above).
