### `for j, r := range string` gives BYTE indices, not rune indices
- **Saw**: a styled banner using multi-byte runes (`█` is 3 UTF-8 bytes) silently lost letters past the first when a cutoff column applied. The unstyled `for j := range []rune(...)` version worked; only the styled path drifted
- **Why**: `for j, r := range str` walks runes for `r` but reports j as the rune's starting BYTE position; `for j := range []rune(str)` gives the rune index. A cutoff computed as a rune position (like `letterEndCols` here) applied against a byte-indexed j drifts: every multi-byte rune past the first pushes j ahead, so `j <= cutoff` goes false too early
- **Fix**: convert to `[]rune(...)` once and iterate the slice — `j` then matches whatever rune-position math the rest of the code uses. Commit `eae5f88`
- **Lesson**: in Go, `range string` is a footgun once positions matter: if they're coordinate-system-sensitive (cutoffs, column-aligned masks), pin one coordinate system end-to-end — the mixed-coordinate bug is invisible at compile time and on ASCII-only content
- **Refs**: `internal/tui/banner.go:renderBanner`
