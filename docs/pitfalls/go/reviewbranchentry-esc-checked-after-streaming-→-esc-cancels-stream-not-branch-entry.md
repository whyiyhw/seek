### reviewBranchEntry Esc checked after streaming → Esc cancels stream, not branch entry
- **Saw**: during an active stream, entering reviewBranchEntry via /review → "Type a branch name…" then pressing Esc cancelled the stream instead; the flag stayed true, so the next Enter submitted `/review <textarea>` (possibly empty).
- **Why**: `handleKey`'s `case tea.KeyEsc` checked `m.streaming` first; the reviewBranchEntry guard was AFTER it, so Esc always hit the streaming-cancel path while a stream was active, regardless of reviewBranchEntry state.
- **Fix**: moved the `m.reviewBranchEntry` check to the top of the Esc handler, before the streaming check, so Esc in branch-entry mode always cancels the entry; the streaming path is only reached when reviewBranchEntry is false.
- **Lesson**: a modal sub-state using the textarea (reviewBranchEntry, setupKeyEntry) must have its Esc guard BEFORE the streaming Esc handler — modal-state Esc guards go first, streaming-cancel second, fallback third.
- **Refs**: `internal/tui/update.go:handleKey` (`case tea.KeyEsc`), `internal/tui/commands.go:handleReviewPick`, commit (this commit)
