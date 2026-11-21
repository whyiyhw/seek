### Legacy conhost (blue PowerShell 5.x window) TUI streaming cascades (use Windows Terminal)
- **Saw**: in blue-background Windows PowerShell 5.x (conhost), assistant streaming text printed a new full-length line on every token — a "staircase" of growing sentences; stray escape bytes appeared in the left margin
- **Why**: seek inline mode relies on ANSI cursor-up + erase to overwrite the live region each frame. Legacy conhost does not handle these sequences reliably; each frame appends instead of overwriting
- **Fix**: document Windows Terminal as the supported Windows TUI host — no seek-side terminal shims. Users install WT via winget / Store / GitHub release and run seek there. Fallback: `seek -p` print mode. See [`docs/guide-windows.md`](guide-windows.md)
- **Lesson**: on Windows, the terminal emulator choice *is* the compatibility layer. Prefer documenting and recommending a modern host over per-emulator code paths in seek
- **Refs**: [`docs/guide-windows.md`](guide-windows.md), [Windows Terminal](https://github.com/microsoft/terminal)
