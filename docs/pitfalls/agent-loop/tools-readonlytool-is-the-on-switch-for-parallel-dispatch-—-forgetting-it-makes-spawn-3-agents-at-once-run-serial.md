### `tools.ReadOnlyTool` is the on-switch for parallel dispatch — forgetting it makes "spawn 3 agents at once" run serial
- **Saw**: two `agent` subagents spawned in one assistant message ran serially — wall time 27s + 21s ≈ 48s instead of the ~27s max.
- **Why**: the dispatch gate ran a tool-call batch concurrently only when EVERY call was backed by a `tools.ReadOnlyTool`; `agent` lacked the marker, so the batch fell to the serial path (since 2026-08 mixed batches partition via `readOnlyCall()`; the marker remains the on-switch).
- **Fix**: `ReadOnly() bool { return true }` on `internal/tools/agent.Tool` (marker feeds the dispatcher, not the permission gate; Manager.Spawn is concurrent-safe), pinned by the compile-time assertion `var _ tools.ReadOnlyTool = (*Tool)(nil)` + `TestTool_ImplementsReadOnlyTool`.
- **Lesson**: `ReadOnlyTool` overloads "doesn't mutate" (permission) and "safe to dispatch concurrently" (orchestrator) — document the stretch, pin with assertion + behavioural test; mark any new fan-out tool.
- **Refs**: `internal/tools/agent/agent.go:ReadOnly`, `pkg/agent/agent.go:readOnlyCall`
