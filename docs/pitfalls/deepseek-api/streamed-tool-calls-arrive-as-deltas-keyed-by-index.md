### Streamed tool calls arrive as deltas keyed by `index`
- **Saw**: first attempt at handling tool calls assumed each chunk contained a complete `tool_call` — wound up with split, garbled JSON in arguments
- **Why**: DeepSeek streams tool calls in fragments: chunk N might emit `{"index":0, "id":"call_1", "function":{"name":"read", "arguments":"{\"pa"}}` and chunk N+1 emits `{"index":0, "function":{"arguments":"th\":\"x\"}"}}`. They must be merged by `index`
- **Fix**: `pkg/agent` accumulates a `map[int]*ToolCall` during the stream, finalising on `EventDone`. Added `Index` field to `deepseek.ToolCall` (omitempty so it doesn't leak into request bodies). Commit `719b84e`
- **Lesson**: streaming tool calls need explicit assembly state. Read the streaming spec, not just the non-streaming one
