### Backticks in raw string literals close the string
- **Saw**: `cmd/seek/main.go:40` suddenly failed `go vet` with `expected ';', found bash` after I added a string mentioning `` `bash ls` ``
- **Why**: the system prompt is a raw string literal (backtick-delimited). The inner backticks closed the string mid-sentence, then the surrounding text became Go syntax
- **Fix**: replaced inner backticks with single quotes (`'bash ls'`). Commit `7daaa3b`
- **Lesson**: raw strings are the cleanest for multi-line content **until** you need backticks inside. Use `"..."` with escapes, or `+`-concatenate, or use single quotes / unicode look-alikes for inline code
