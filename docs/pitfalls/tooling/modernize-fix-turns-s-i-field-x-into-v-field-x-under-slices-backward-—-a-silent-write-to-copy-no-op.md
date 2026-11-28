### modernize -fix turns `s[i].Field = x` into `v.Field = x` under slices.Backward — a silent write-to-copy no-op
- **Saw**: 第 3 刀 modernize（c_l3cw6d）：gopls v0.23.0's slices.Backward fix rewrote `AttachPredictedNext`'s `a.messages[i].PredictedNext = text` into `v.PredictedNext = text` (`v` = the range COPY) — prediction stopped persisting while `go build` / `go vet` / `go test -race ./...` stayed green (zero coverage).
- **Why**: the Backward fixer replaces `s[i].X` with `v.X` uniformly, dropping the index; it doesn't distinguish reads from writes, and v0.23.0 has no write-safety guard.
- **Fix**: audited all 12 Backward rewrites in the diff (the only write-through-value); kept Backward, took the index (`for i, v := range slices.Backward(...)`, write via `a.messages[i]`), added `TestAttachPredictedNextWritesThroughIndex`.
- **Lesson**: never trust a bulk `-fix` — grep the diff for value-dropping rewrites (`range slices.Backward`, index elision) and check each loop body for writes via the loop var; a green suite only means nothing was watching.
- **Refs**: `pkg/agent/agent.go` `AttachPredictedNext`, `pkg/agent/agent_test.go`, `docs/notes-slim-candidates-2026-11.md` 执行状态·第 3 刀
