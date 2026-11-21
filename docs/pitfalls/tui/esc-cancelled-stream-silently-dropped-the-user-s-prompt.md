### Esc-cancelled stream silently dropped the user's prompt
- **Saw**: pressing Esc mid-stream to cancel an assistant's response also erased the user's prompt from the input box. The user had to re-type it to re-submit
- **Why**: the Esc handler in `handleKey` cleared `queuedText` ("" = empty string) to "stop everything", but the user's submitted prompt was already sitting in `queuedText` at that point. The textarea had been cleared on Enter, so nothing was recoverable
- **Fix**: Esc now restores `queuedText` into the textarea before clearing it; `streamEndMsg` also restores `promptHistory` into the textarea if the stream was cancelled and the input is still empty. Commit `af4b4d0`
- **Lesson**: when you clear volatile state on user-cancel, check whether that state contains something the user would want back. "Cancel the current operation" and "clear the edit buffer" are different intents — treat them separately
- **Refs**: `internal/tui/update.go:handleKey`, `internal/tui/update.go:streamEndMsg`
