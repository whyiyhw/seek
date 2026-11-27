### Agent.Messages() races with the Prompt goroutine when called from TUI (Ctrl+C, streamEnd)
- **Saw**: CI `-race` failure when Ctrl+C lands during a streaming turn: `persistSession` (`update_key.go:226`) calls `Agent.Messages()`, copying `a.messages` unsynchronised while the Prompt goroutine appends — in production this can torn-slice corrupt the saved session.
- **Why**: `Agent` had a `sync.RWMutex` field that was never used; `Messages()` did a plain `len()`+`copy()` and all `a.messages = append(...)` writes in Prompt were unprotected, while `persistSession` runs on the TUI goroutine (Ctrl+C, streamEnd, approval).
- **Fix**: `mu.RLock()`/`mu.RUnlock()` in `Messages()`, `mu.Lock()`/`mu.Unlock()` in `Reset()`, and an `appendMessage()` helper that locks before appending; all Prompt write sites now use it.
- **Lesson**: "NOT safe for concurrent calls" is a promise to callers, not a defence — if a public method has any cross-goroutine caller, add the lock before CI `-race` finds it.
- **Refs**: `pkg/agent/agent.go`, `internal/tui/update_agent.go:persistSession`, `internal/tui/update_key.go:226`
