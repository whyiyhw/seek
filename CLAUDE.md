# seek — project guide for AI assistants

This file is read at the start of every Claude Code session in this repo. It mirrors [`AGENTS.md`](AGENTS.md), which **seek** itself auto-loads at startup; keep both in sync when editing either.

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
3. `read` accepts an optional `limit` parameter (default 50, max 50 — values above 50 error). Use `offset=N` to page through larger files.
4. `grep` caps at 20 matches by default (`max_matches` can be raised, but rarely should be).

Never read a whole file to answer a question you could answer with grep. The prefix cache survives only when old messages are byte-identical; lazy whole-file reads balloon prompt tokens and degrade cache hit rate.

5. **read before edit** — before calling `edit`, first `read(offset=N, path=...)` on the target lines to capture the **exact whitespace** of the `old_string`. Do not guess tab depth from memory; the read output preserves it byte-for-byte. A single read call costs less than the error-fix loop from a mismatched `old_string`.

## Token & prefix-cache constraints (non-negotiable)

DeepSeek charges ~10× less per token on cache hits. The cache key is an **exact byte sequence** of the entire prior message history. This means:

- **Never modify old messages before sending to the API** — even "compression" or "summarisation" of old tool results invalidates the cache and can cost more than sending the full history.
- **Token optimisation happens at write-time** (tool output size limits), not at send-time (in-transit mutation).
- Tool output limits are enforced inside the tool itself. Do not add a post-hoc filter layer in `pkg/agent` or the API path.

## Permission model

`internal/permission.Policy` is the single gate every dangerous tool action goes through. Read `permission.go` once before changing any gating logic; the four modes + two flags are easy to misread.

- **Four modes**: `ModeDeny` (refuse everything dangerous, used in print mode), `ModeAsk` (consult `askFn` per call — the default TUI mode), `ModeYolo` (allow everything; `--yolo` or session escalation), `ModePlan` (strict read-only; `--plan` / `/plan`). Mode is the coarse axis.
- **Two flags layered on top**:
  - `preApproved` — set by the `plan` tool when the user picked "approve with auto-approve-per-step" on `propose` AND a `plan(start=N)` call entered a step. Lets write/edit/bash inside that step auto-pass without `askFn`. Cleared on step `complete`/`skip`, Esc cancellation, `SetMode` (any mode change), `/plan` off. **Only meaningful inside `ModeAsk` (plan-execute substate)** — `ModePlan` still denies writes even with `preApproved=true` (defense in depth).
  - `Action.ReadOnly` — set by the bash tool when the command matches a side-effect-free whitelist (`go vet` / `go list` / `npm ls` / `make -n` / …) AND has zero shell metacharacters. Lets `ModePlan`'s `KindBash` branch through. Treat it as advisory-from-the-tool, NOT something callers can lie about — the bash tool's `readonly.go` is the only authorised setter.
- **Denials are tool results, not fatal errors** (`internal/permission.ErrDenied`). The agent feeds the message verbatim back to the LLM so it can ask the user or reconsider. NEVER bypass this flow — bypassing means the model sees a `ToolExecEnd` failure instead of structured "denied, here's why", and loses the ability to recover the turn.
- **Mode + flag transitions default safe**. `SetMode` clears `preApproved`. Cancel / Esc / `/plan` off clear `preApproved`. New propose-approval starts with `preApproved=false` and arms it only via explicit `plan(start)`. The invariant is: a half-finished step never leaves the gate unlocked across the next user prompt.
- **Adding a new mode is expensive.** It touches `permission.go`, every tool that calls `Check`, every mode-label site (status bar / mode reminder / cmdPlan / cmdYolo), every test that constructs a `Policy`. Before adding one, exhaust the flag approach — that's how `preApproved` and `ReadOnly` were added without a new mode.

## Code conventions

- **Stdlib first**. `pkg/deepseek` has zero external deps and should stay that way. New deps live behind sensible boundaries.
- **Tool JSON schemas are package-level `[]byte` constants**, not built at call time. Identical bytes across turns is what lets DeepSeek's prefix cache hit (`PRD §4.8.1`).
- **Tool schemas must not expose parameters that let the model bypass output size limits.** Examples: `read` has a `limit` field (default 50, max 50 — values above 50 error); `grep` has `max_matches` defaulting to 20; `propose` has `maxItems: 20` on `steps` (`maxSteps` const); the `plan` tool's `index` is `minimum: 1` and runtime-checked against the active plan's step count. If a limit is safety-critical, reject values above the max with a clear error.
- **New tools** live at `internal/tools/<name>/` with a `New(...)` constructor. If the tool can mutate the filesystem or shell, inject a `*permission.Policy`. If the tool wants user choice (picker / approval), inject `*askuser.Policy`.
- **Sink interfaces: don't break the main contract — add OPTIONAL sibling interfaces.** When a new feature needs more context flowing into a tool's Sink, do NOT add parameters to the existing Sink method (every fake / recording sink in tests breaks). Define a sibling interface (`type FooReporter interface { Foo() ... }`) and upcast at the call site: `if r, ok := sink.(FooReporter); ok { r.Foo() }`. The propose tool currently carries four such optional interfaces (`DuplicateChecker`, `ProgressReporter`, `ContextReceiver`, `ArtifactReporter`); none required a Sink signature change. See `docs/pitfalls.md` "Plan artifact write needs context BEFORE Sink.Approved fires" for the rationale.
- **Wire-format strings are contracts when something parses them.** A tool result string like `[plan: approved]` looks like prose, but `internal/tools/plan/reconstruct.go` scans for that prefix verbatim to seed plan-state on resume. Once a marker has a parser, the marker is wire format — additions go AFTER the closing token (`[plan: approved] (auto-approve-per-step)`), never inside it (`[plan: approved batch]` would silently break reconstruction). When introducing a new variant of an already-parsed result, add a test that asserts on `HasPrefix(out, "<marker>")` so future variants can't slip past.
- **Permission denials are tool results, not fatal errors** (`internal/permission.ErrDenied`). The agent feeds the message back to the LLM so it can ask the user — never bypass that flow. See the "Permission model" section above for the full denial contract.
- **Session format is JSONL** (`schema_version=2`, `.jsonl` extension): line 1 is the header (all scalar metadata), lines 2..N are one `deepseek.Message` per line. Legacy `.json` files (schema_version ≤ 1) are migrated on next `Save`. `loadMeta()` reads only line 1 — do not call `Load()` when you only need metadata.
- **Reconstruct state from transcript, don't persist parallel state files.** Plan state machine is the canonical example: `propose` args + `plan(start|complete|skip)` calls in the transcript are the single source of truth; `seek -resume` replays them via `reconstruct.go`. Resist the urge to add a sibling `.json` / `.md` file that mirrors runtime state — you'll fight drift forever. Artifact files (`~/.seek/projects/<id>/plans/`) are the exception: write-once human-readable snapshots, NOT state.

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
