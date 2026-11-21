<!-- pitfalls-absorbed: original-section: - -->
### [已吸收] Streaming kept yanking the viewport to the bottom
- **Saw**: scrolling up to read earlier output mid-stream would snap back to the bottom on the next token
- **Why**: every `agentEventMsg` unconditionally called `viewport.GotoBottom()`
- **Fix**: capture `wasAtBottom := m.viewport.AtBottom()` before applying the event; only `GotoBottom()` if true. Commit `7daaa3b`
- **Lesson**: auto-scroll should respect user intent — if they've scrolled away, that's an implicit "stop following"
- **已吸收**: viewport.GotoBottom/AtBottom went with the viewport widget — native terminal scrollback cannot be "yanked" → `internal/tui/update.go` "viewport widget is gone" note
