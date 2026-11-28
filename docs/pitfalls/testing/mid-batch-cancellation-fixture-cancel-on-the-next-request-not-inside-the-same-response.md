### Mid-batch cancellation fixture: cancel inside the SAME response kills the row you thought had landed — cancel on the NEXT request
- **Saw**: `TestBatch_MidBatchCancellation` (`internal/judge`) cancelled the ctx from inside the fake handler right after writing the first reply, expecting row 0's verdict; it came back `judge call: … context canceled` — the "first reply already landed" assumption was false.
- **Why**: `net/http` hands control back when the handler returns, but the client finishes consuming only after body read + decode; a `cancel()` inside the handler races the client's read and can abort the response it just served — "server answered" ≠ "caller consumed".
- **Fix**: count arrivals in the fake handler and cancel on the **second** request — by then `Batch` has returned row 0's verdict and is issuing row 1, the mid-batch state. `internal/judge/judge_test.go:TestBatch_MidBatchCancellation`.
- **Lesson**: to test "interrupt at state N", drive the interrupt from the request that BEGINS state N+1, not the response that ends state N; same for any "K successful calls then stop" fixture.
- **Refs**: `internal/judge/judge_test.go`, `internal/judge/judge.go:Batch`
