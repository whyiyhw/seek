### `finish_reason="length"` looks like a normal stop
- **Saw**: long analysis responses were silently truncated; the TUI appeared to finish normally, the model's answer was mid-sentence, and the user had no indication that the response was cut
- **Why**: `finish_reason="length"` means the model hit `max_tokens` and stopped generating — not that the answer was complete. The original code treated `"length"` identically to `"stop"`
- **Fix**: `runTurnDeepSeek` now emits an `ErrorEvent` on `finish_reason="length"`: "response truncated (finish_reason=length, max_tokens=N) — use /compact to free context or ask me to continue". Also raised `MaxTokens` default from server default (~4096) to 8192. Commit `e240ea5`
- **Lesson**: check every possible `finish_reason` value against the API docs; "stop" is the only one that means "normal completion". All others need surfacing
