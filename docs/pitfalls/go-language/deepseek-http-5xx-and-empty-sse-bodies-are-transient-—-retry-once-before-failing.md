### DeepSeek HTTP 5xx and empty SSE bodies are transient — retry once before failing
- **Saw**: (a) `deepseek api error: internal_error: Internal Server Error` from `ChatStream`; (b) the stream closed with no deltas, tripping the empty-response guard.
- **Why**: `pkg/deepseek.ChatStream` had no retry layer, so transient blips became hard errors.
- **Fix**: one-shot retry in `pkg/deepseek/stream.go` for 5xx/transport errors before the channel exists and for SSE closing with no data delta; 500ms backoff, gated on `emittedAny == false` (a re-send after one delta duplicates), no 4xx retry; tests cover both triggers, the cap, post-emit lockout, ctx-cancel.
- **Lesson**: one outage class at two layers needs both retried; `emittedAny` makes transparent retry safe — never retry after the caller saw anything.
- **Refs**: `pkg/deepseek/retry.go:retryCall` (also `Chat`/`FIM`), `pkg/deepseek/stream.go:pumpChatStream`, `pkg/deepseek/stream_test.go:TestChatStream_RetryOn500`
