### "Zero tools" is unreachable through a whitelist API whose empty list means "no filter"

- **Saw**: `-tools` could not express a zero-tool request: `parseToolsFlag("")` → empty slice → `Registry.Whitelist` early-returns (empty keep = no-op, the "absent = full default registry" contract), so the FULL registry stayed; the smallest isolatable surface was one named tool.
- **Why**: "I did not supply a filter" (empty list) and "I want nothing" shared one code path — a list-valued flag can't encode "none" by being empty because emptiness is already load-bearing.
- **Fix**: explicit sentinel `-tools none` → `Registry.Clear()` (new method; `Whitelist`'s empty-slice no-op untouched, so the v8·T1 bare-core lane is byte-identical); sentinel is whole-value: `none,read` exits 2 ("sentinel, not a tool"). (card c_lh6tlq)
- **Lesson**: when an API's zero/empty value already means "unset", "explicitly nothing" needs its OWN spelling (sentinel or separate method) — don't widen the empty case, and give the sentinel a whole-value check; test the wire (`"tools":` absent from request bytes).
- **Refs**: `internal/tools/tool.go` (`Clear`), `cmd/seek/main.go` (`toolsNoneSentinel`, `applyToolsWhitelist`), `cmd/seek/tools_flag_test.go`, `docs/prd/feature-judge-engine.md` §2.2 缺口①
