### Fake SSE backend embedding user text must json.Marshal, not Sprintf
- **Saw**: a real-binary ACP e2e answered `session/prompt` with `-32000 openai: decode chunk: unexpected end of JSON input` — the error names the lane's SSE parser, not the test's backend.
- **Why**: seek appends per-message reminders (workflow/mode notes) to user content WITH newlines; the fake built its chunk with `fmt.Sprintf(`{"...":{"content":"seen=%d:%s"}}`, …)` — raw newlines split the `data:` frame, so the client parsed a truncated literal.
- **Fix**: build any request-derived chunk content with `json.Marshal` (escapes newlines/quotes) before writing the `data:` line (commit c_5flg7r).
- **Lesson**: in SSE fakes, request-fed payload is newline-guaranteed (reminders) — Marshal, never Sprintf into JSON. Next-door harness rule: a test defer blocking on a channel drained from the child's stdout deadlocks BEFORE t.Cleanup can kill the child (whole-suite timeout) — never wait on the drain goroutine.
- **Refs**: `cmd/seek/acp_multi_e2e_test.go` (multiSessionBackend handler); compare `cmd/seek/acp_mode_e2e_test.go`'s static literals, safe because they embed no request-derived text.
