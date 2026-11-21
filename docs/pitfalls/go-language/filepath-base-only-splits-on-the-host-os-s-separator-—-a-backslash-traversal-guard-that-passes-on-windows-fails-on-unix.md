### `filepath.Base` only splits on the host OS's separator — a backslash traversal guard that passes on Windows fails on unix
- **Saw**: `TestPath_TraversalGuard` failed on macOS: `assets.Path("/tmp", "a\b.png")` was accepted, while the same guard rejects it on Windows
- **Why**: the guard was `asset != filepath.Base(asset)` — but `filepath.Base` treats `\` as a separator ONLY on Windows. On unix `a\b.png` IS its own Base, so the check passes. Asset names round-trip through session JSONL (portable wire data): a name written on macOS and opened on Windows would traverse
- **Fix**: added `strings.ContainsAny(asset, "/\\")` to the guard so both separators are rejected on every OS. `internal/assets/assets.go:Path`
- **Lesson**: for portable identifiers, never derive a path-safety check from the HOST's path semantics (`filepath.Base`, `filepath.IsAbs`, …) — reject both separator characters explicitly. The test suite only catches this if it runs the foreign-OS case on every OS, which is exactly what the literal `a\b.png` case does
- **Refs**: `internal/assets/assets.go`, `internal/assets/assets_test.go:TestPath_TraversalGuard`
