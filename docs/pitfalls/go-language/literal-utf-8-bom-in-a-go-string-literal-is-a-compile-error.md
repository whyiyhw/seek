### Literal UTF-8 BOM in a Go string literal is a compile error
- **Saw**: `internal/skill/skill.go` failed to build with `illegal byte order mark (syntax)` at the line `strings.TrimPrefix(string(data), "<BOM>")`
- **Why**: Go's scanner permits a BOM only at the very start of a source file. Anywhere else — even inside a string literal — it's rejected as a stray BOM, not a Unicode codepoint
- **Fix**: use the escape `"﻿"` instead of pasting the BOM byte. Commit `2c53248`
- **Lesson**: when you need a special invisible character in source, write it as an escape. Pasting "the thing" from a doc is exactly the foot-gun the scanner is protecting you from
- **Refs**: `internal/skill/skill.go:Parse`
