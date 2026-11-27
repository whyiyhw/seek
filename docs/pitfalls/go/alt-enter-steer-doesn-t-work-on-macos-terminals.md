### Alt+Enter steer doesn't work on macOS terminals
- **Saw**: pressing Option+Enter during a stream queued the text instead of steering it — same behaviour as plain Enter; the `Alt` modifier was lost before it reached bubbletea.
- **Why**: `msg.Alt` depends on the terminal sending `\x1b\r` (ESC prefix + Enter), but macOS terminal emulators (Terminal.app, iTerm2, Warp) typically send Option+Enter as a bare `\r` unless "Use Option as Meta key" is enabled, so `msg.Alt` is false and the code falls into the queue branch (update.go:502).
- **Fix**: added `/steer [text]` slash command (alias `/s`) with queue promotion — bare `/steer` promotes a queued message to steer, `/steer <text>` is equivalent to Alt+Enter. Commit `0e5b275`
- **Lesson**: modifier-key-dependent keybindings (Alt+key) are unreliable because terminal emulators vary in how they encode the modifier; for every modifier-gated action provide a text-based alternative (slash command), and "queue first, then promote" requires no modifier at all.
- **Refs**: `internal/tui/update.go:502` (msg.Alt check), `internal/tui/commands.go:cmdSteer`, commit `0e5b275`
