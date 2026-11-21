### Gemini omits tool call IDs; system message is `systemInstruction`, not a messages-array entry
- **Saw**: two surprises at once when wiring Gemini: (1) function calls in the SSE stream had no `id` field, breaking the call-result pairing that every other provider relies on; (2) sending a `role="system"` message in the `contents` array returned 400
- **Why**: (1) Gemini's `functionCall` part schema has `name` and `args` but no ID field — it assumes you correlate by turn position. (2) System context goes in a separate top-level `systemInstruction` field, not in `contents`
- **Fix**: (1) IDs are generated deterministically as `gemini_0`, `gemini_1`, … by incrementing a `toolIdx` counter during SSE parsing. (2) `buildGeminiRequest` inspects the first message: if `role=="system"` it extracts the content to `SystemInstruction` and shifts the rest into `contents`. Commit `a1fde05`
- **Lesson**: read the Gemini API shape end-to-end, not the OpenAI-compat docs. Function calling and system instructions are different enough that assuming an OpenAI mental model will bite you on both
- **Refs**: `pkg/llm/provider/gemini/client.go:buildRequest`, `client.go` SSE parser
