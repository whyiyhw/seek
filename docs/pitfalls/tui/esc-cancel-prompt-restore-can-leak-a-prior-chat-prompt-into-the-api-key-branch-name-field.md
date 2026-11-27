### Esc-cancel prompt restore can leak a prior chat prompt into the API-key / branch-name field
- **Saw**: after Esc-cancelling a streaming turn, opening `/setup` restored the prior chat prompt into the textarea and Enter saved it as the API key into `~/.seek/config.json`; same path for `/review` branch-name entry.
- **Why**: `handleStreamEnd` unconditionally restores `m.promptHistory[last]` into `m.input` when the textarea is empty (line 68), with no guard for `m.setupKeyEntry` / `m.reviewBranchEntry` — the branches that read the input as API key / branch name.
- **Fix**: added `!m.setupKeyEntry && !m.reviewBranchEntry` guards to the restore condition, plus paste-fold resolution in the same two branches (a folded paste marker would otherwise be read as the value).
- **Lesson**: when a background event handler writes into a shared textarea, check which input mode is active — the Esc-cancel restore must be a no-op when the textarea serves another purpose.
- **Refs**: `internal/tui/update_agent.go:68`, `internal/tui/update_key.go` (reviewBranchEntry + setupKeyEntry branches)
