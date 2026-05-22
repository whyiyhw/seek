# seek — project guide for AI assistants

This file is auto-loaded by **seek** at startup (and by other agent tools that follow the `AGENTS.md` convention). The canonical agent instructions for this repo live here. [`CLAUDE.md`](CLAUDE.md) mirrors this content for Claude Code; keep both in sync.

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
- See [`docs/prd/`](docs/prd/) for the full PRD series (v0: initial, v1: current development).

## Tool usage workflow (load-bearing)

When exploring code, follow this order — skipping steps costs tokens and breaks prefix cache:

1. **grep first** — find exact file + line before reading anything.
2. **read(offset=N) second** — read only the relevant window, not the whole file.
3. `read` has **no `limit` parameter** — it was intentionally removed from the schema so the model cannot bypass the fixed 50-line window. Passing `limit` is rejected as an unknown field. Use `offset=N` to page through larger files.
4. `grep` caps at 20 matches by default (`max_matches` can be raised, but rarely should be).

Never read a whole file to answer a question you could answer with grep. The prefix cache survives only when old messages are byte-identical; lazy whole-file reads balloon prompt tokens and degrade cache hit rate.

## Token & prefix-cache constraints (non-negotiable)

DeepSeek charges ~10× less per token on cache hits. The cache key is an **exact byte sequence** of the entire prior message history. This means:

- **Never modify old messages before sending to the API** — even "compression" or "summarisation" of old tool results invalidates the cache and can cost more than sending the full history.
- **Token optimisation happens at write-time** (tool output size limits), not at send-time (in-transit mutation).
- Tool output limits are enforced inside the tool itself. Do not add a post-hoc filter layer in `pkg/agent` or the API path.

## Code conventions

- **Stdlib first**. `pkg/deepseek` has zero external deps and should stay that way. New deps live behind sensible boundaries.
- **Tool JSON schemas are package-level `[]byte` constants**, not built at call time. Identical bytes across turns is what lets DeepSeek's prefix cache hit (`PRD §4.8.1`).
- **Tool schemas must not expose parameters that let the model bypass output size limits.** Example: `read` has no `limit` field; `grep` has `max_matches` but it defaults to a conservative 20. If a limit is safety-critical, make it non-overridable.
- **New tools** live at `internal/tools/<name>/` with a `New(...)` constructor. If the tool can mutate the filesystem or shell, inject a `*permission.Policy`.
- **Permission denials are tool results, not fatal errors** (`internal/permission.ErrDenied`). The agent feeds the message back to the LLM so it can ask the user — never bypass that flow.
- **Session format is JSONL** (`schema_version=2`, `.jsonl` extension): line 1 is the header (all scalar metadata), lines 2..N are one `deepseek.Message` per line. Legacy `.json` files (schema_version ≤ 1) are migrated on next `Save`. `loadMeta()` reads only line 1 — do not call `Load()` when you only need metadata.

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
