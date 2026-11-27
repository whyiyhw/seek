### `reasoning_effort` values are `high|max` on DeepSeek V4 — not OpenAI's `low|medium|high`
- **Saw**: the comment on `ChatRequest.ReasoningEffort` claimed `"low"|"medium"|"high"` (OpenAI o-series carry-over) — a 3-rung picker would expose values DeepSeek may silently ignore or reject.
- **Why**: DeepSeek V4 documents only `high` and `max` alongside `thinking.type` — not OpenAI's trio.
- **Fix**: corrected `pkg/deepseek/types.go:103` to document `high|max`; `/effort` exposes `off|high|max` only; `internal/tools/think` keeps `high` baseline, bumps to `max` at `high`. Pins: `TestAgent_EffortOverridesThinking`, `TestThink_BumpEffort`.
- **Correction (2026-11-24, c_trrsn7)**: the create-chat-completion reference documents `none | low | high | max` (medium/xhigh compat aliases → `high`) — the enum was wrong the other way; `low` is the real level a one-shot judge batch wants. `low` was added to the CLI's `--effort` (startup flag, batch lanes only); the TUI `/effort` picker stays `off|high|max` so an interactive session can't pin an unpicked depth.
- **Lesson**: a Go field name matching a sibling provider's parameter does not make the value enum match — re-read the vendor's doc for the value set every time.
- **Refs**: `pkg/deepseek/types.go`, `internal/tools/think/think.go:bumpEffort`, `internal/tui/commands.go:effortChoices`, `cmd/seek/main.go:effortLevels`
