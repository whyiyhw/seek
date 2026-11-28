### Routing built-in agent types through the custom gate silently widens their toolset
- **Saw**: (P2-c, caught pre-landing) unifying `Manager.resolveType` so built-ins flow through `ResolveCustomType` would spawn `explore` with ~10 tools including bash/write/edit in any session whose registry lacks `webfetch` (env-gated) — silent widening of a read-only type.
- **Why**: D13's unknown-tools rule (any whitelist tool missing from the parent registry → drop the whitelist, degrade to the general-purpose toolset) is the loud-config-error rule for FILE types; built-ins always intersected with the parent inside `Template.Filter`. The rules coexist; ORIGIN decides which applies.
- **Fix**: `resolveTypedDef` (manager.go) applies the degrade only for `OriginProject`/`OriginUser`; builtin + config clones keep Filter's intersect. `TestSpawn_ConfigOverrideRoutesBuiltin` pins it with a webfetch-less parent registry.
- **Lesson**: "route everything through one gate" migrations must diff the SEMANTICS of each old path, not the code shape — where they differed, the shared gate needs origin-aware rules.
- **Refs**: `internal/subagent/manager.go`; `docs/prd/feature-orchestration.md` §5 D13.
