# seek — project guide for AI assistants

This file is read at the start of every Claude Code session in this repo. Treat the instructions below as mandatory project conventions.

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
- See [`PRD.md`](PRD.md) for the full design and the milestone plan.

## Code conventions

- **Stdlib first**. `pkg/deepseek` has zero external deps and should stay that way. New deps live behind sensible boundaries.
- **Tool JSON schemas are package-level `[]byte` constants**, not built at call time. Identical bytes across turns is what lets DeepSeek's prefix cache hit (`PRD §4.8.1`).
- **New tools** live at `internal/tools/<name>/` with a `New(...)` constructor. If the tool can mutate the filesystem or shell, inject a `*permission.Policy`.
- **Permission denials are tool results, not fatal errors** (`internal/permission.ErrDenied`). The agent feeds the message back to the LLM so it can ask the user — never bypass that flow.

## Testing

- `go test ./...` before every commit. CI runs `-race` on three OSes.
- Tests use `httptest` fake DeepSeek backends — no real API key required for the suite.
- For real-API smokes, write the key to `.env` (gitignored) and source it; never put it on the command line.

## Commit messages

- Subject in conventional-commit style: `feat(M3): ...`, `fix(tui): ...`, `chore: ...`.
- Body explains **why**, not what. The diff already shows what.
- `Co-Authored-By: <model name> <noreply@anthropic.com>` trailer on AI-written commits.
- `Pitfall: <summary>` trailer when fixing a non-obvious bug (see above).
