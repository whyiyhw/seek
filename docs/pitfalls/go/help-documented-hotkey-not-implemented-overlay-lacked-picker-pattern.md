### `/help` documented `?` hotkey not implemented; overlay lacked picker pattern
- **Saw**: the overlay documented `"/help, /? or ?", "Show this help"` but bare `?` in empty input did nothing; `/help` used a blocking full-height overlay while `/model` used a compact ↑/↓ picker.
- **Why**: `handleKey` had no case for a bare `?` (the docstring was aspirational), and `cmdHelp` used a static overlay instead of the picker pattern (`modelPickerOpen` + `modelPickerFiltered`).
- **Fix**: added the `?` hotkey in `handleKey` on empty input (before the textarea); `cmdHelp` opens a topic picker on no-args (all, commands, keys, about) with the overlay only on selection or arg; `/help ` auto-open added in `updateCommandMenu` mirroring `/model /effort`.
- **Lesson**: don't document shortcuts that don't exist; a command paralleling an existing one (`/model` → picker) should match its interaction pattern — a full-height overlay is a different ask than a compact dropdown.
- **Refs**: `internal/tui/update_key.go`, `internal/tui/commands.go`, `internal/tui/update_menu.go`, `internal/tui/commands_test.go`
