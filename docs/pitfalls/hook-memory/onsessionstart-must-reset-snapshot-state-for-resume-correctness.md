### OnSessionStart must reset snapshot state for --resume correctness
- **Saw**: after M5.9's snapshot+delta strategy, `--resume` injected a stale snapshot from the previous session's first `OnPrePrompt`, because `snapshotInjected` was still `true` from the prior agent lifetime
- **Why**: Hook fields `snapshotInjected` and `snapshotEntryNames` survive agent lifetimes when the registry is reused (`--resume`). Without explicit reset, the second session's first `OnPrePrompt` skips `injectSnapshot()` and produces no soul/index context — the model loses all memory context
- **Fix**: `OnSessionStart` resets `snapshotInjected = false` and `snapshotEntryNames = nil` before GC, so the first `OnPrePrompt` after every session boundary (fresh or resume) rebuilds the snapshot from current data. Commit (M5.9)
- **Lesson**: any Hook field tracking per-session state MUST be reset in `OnSessionStart` — the Hook is reusable across agent lifetimes (--resume), not 1:1 with sessions; zero-init at construction doesn't cover all paths
- **Refs**: `internal/memory/hook.go:OnSessionStart`, `internal/memory/hook.go:Hook.snapshotInjected`
