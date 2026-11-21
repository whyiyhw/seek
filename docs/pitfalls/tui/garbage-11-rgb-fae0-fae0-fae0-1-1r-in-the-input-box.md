### Garbage `]11;rgb:fae0/fae0/fae0\[1;1R` in the input box
- **Saw**: opening the TUI, the input field had escape-sequence-looking junk pre-filled; pressing Enter would send it to the LLM
- **Why**: `glamour.WithAutoStyle()` lazily probes the terminal background via an **OSC 11** query (`ESC ] 11 ; ? ESC \`). The terminal's response (`ESC ] 11 ; rgb:... ESC \`) plus a stray cursor-position report would arrive on stdin **after** bubbletea had taken over the TTY, so they were parsed as keystrokes and inserted into the textarea
- **Fix**: in `cmd/seek` we now call the background probe **before** entering bubbletea's alt-screen (originally `termenv`, since the v2 migration `lipgloss.HasDarkBackground`). The query/response handshake completes synchronously while we still own stdin. The result is passed as `Options.GlamourStyle` and glamour gets `WithStandardStyle(style)` (no runtime query). Commit `24342b3`
- **Lesson**: any library that auto-probes the terminal must run before your TUI grabs the TTY, or its probe responses will leak as input. Env override `SEEK_STYLE=dark|light` for users with weird terminals
- **Refs**: `cmd/seek/main.go:detectGlamourStyle`
