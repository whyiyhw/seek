### agenttypes.Definition zero-Origin reads as a built-in — hand-built Sets silently lose their types
- **Saw**: a test Set built via `agenttypes.NewSet().Add(&Definition{...})` spawned as `unknown subagent_type` — the def was in the Set, spell-checked, everything.
- **Why**: `Origin` zero value IS `OriginBuiltin`, and the spawn resolver discriminates custom-vs-builtin by `def.Origin != OriginBuiltin` (file defs always get an explicit origin from `Load`; only hand-built ones don't). Zero-Origin defs fall through to the compile-time template table, miss, and read as unknown.
- **Fix**: test builder sets `Origin: OriginProject` explicitly (`internal/subagent/custom_test.go` `def()` helper, comment explains). Loud failure, never a widening — but a minute lost to it.
- **Lesson**: when a sentinel enum's zero value is a meaningful category, constructors that bypass the loader must set it explicitly; consider making the zero value the "unset/invalid" case for new enums.
- **Refs**: `internal/agenttypes/agenttypes.go` (`Origin`), `internal/subagent/manager.go` (`resolveType`)
