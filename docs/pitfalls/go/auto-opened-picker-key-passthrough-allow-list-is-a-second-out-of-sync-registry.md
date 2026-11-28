### Auto-opened picker key-passthrough allow-list is a second, out-of-sync registry
- **Saw**: typing `/skill use pl` then Backspace appeared to freeze the UI — Tab/Enter/↑/↓/Esc worked but Backspace and typing did nothing; same for `/review` and `/help` auto-opened pickers.
- **Why**: `update_key.go`'s `switch m.pickerPurpose` at line 210 lists which picker purposes pass Backspace/Runes/Space to the textarea; `skill-verb`, `skill-name`, `review`, `help-topic` were never added, so those keys hit the `default` swallow-all branch: the textarea never changed, `updateCommandMenu` couldn't dismiss the picker.
- **Fix**: added `"review", "skill-verb", "skill-name", "help-topic"` to the allow-list in `update_key.go:210` and `"help-topic"` to the stale-cleanup condition in `update_menu.go:219` (also missing it). Closes #4.
- **Lesson**: a new auto-opened picker touches three uncoupled sites — Branch 1 trigger and Branch 2 stale-cleanup in `update_menu.go`, plus the key-passthrough allow-list in `update_key.go`; miss one and the picker silently freezes — grep the last picker added and match all three.
- **Refs**: `internal/tui/update_key.go:210`, `internal/tui/update_menu.go:133,182,219`
