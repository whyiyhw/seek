### V4-Flash prediction returns empty content when Thinking isn't explicitly disabled
- **Saw**: side-channel suggested-reply (`Suggest`) always returned `""` despite 200 OK and consumed tokens: `finish_reason="length"`, `content=""`, `reasoning_content` populated — all `max_tokens` spent on reasoning.
- **Why**: the DeepSeek API defaults V4-Flash to thinking-mode-*on* at the endpoint level even though `ShouldEnableThinking("deepseek-v4-flash")` returns false in the code; without an explicit `Thinking: {Type: "disabled"}` the model burns the entire `max_tokens` on `reasoning_content` and emits empty `content`.
- **Fix**: set `Thinking: &deepseek.ThinkingMode{Type: "disabled"}` in the `Suggest` function's `ChatRequest` — the prediction task (short single-sentence guess) needs zero chain-of-thought.
- **Lesson**: never assume the API default matches the code's `ShouldEnableThinking` switch (V4 models may default thinking-on server-side); for fast/cheap tasks (side-channel prediction, classification) always pin `Thinking: disabled` explicitly.
- **Refs**: `internal/suggester/suggester.go`, `pkg/deepseek/types.go:ShouldEnableThinking`, `pkg/deepseek/types.go:ThinkingMode`
