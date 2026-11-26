### `s[:n]` on a multi-byte UTF-8 string produces broken runes
- **Saw**: queued Chinese text (Enter during a stream) showed garbled characters in the "↰ queued:" preview, and any Chinese clipped by `truncateOneLine` rendered as invalid UTF-8 (�) in tool args and permission prompts.
- **Why**: `truncateOneLine` used `len(s)` (byte count) and `s[:n]` (byte slicing) on UTF-8; Chinese characters are 3 bytes, so `n` landing mid-sequence leaves an incomplete rune — same root cause as the banner bug, byte-level indexing on a multi-byte string.
- **Fix**: changed `truncateOneLine` to use `[]rune(s)` for length comparison and slicing — rune-level slicing never splits a multi-byte character. Commit (this commit)
- **Lesson**: any Go code that truncates, clips, or paginates user-visible text must use `[]rune` for the count and the slice — `string` indexing is byte-level and silently corrupts multi-byte characters (Chinese, emoji, CJK punctuation); "n chars" in the signature must count chars, not bytes.
- **Refs**: `internal/tui/update.go:truncateOneLine`, sibling entry "`for j, r := range string` gives BYTE indices, not rune indices" (same root cause, different surface)
