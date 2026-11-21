### edit tool byte-matches fail on Windows CRLF Go files when old_string uses LF
- **Saw**: on Windows, `edit` with a multi-line `old_string` copied from model output (LF `\n`) against a CRLF Go file always fails with `expected 1 replacements but old_string occurs 0 times`, even when the visible text is identical; agents fall back to Python/PowerShell one-off scripts
- **Why**: `edit` tier-1 matching is exact bytes. Windows Go files default to `\r\n`; model-produced needles use `\n`. Single-line edits without embedded newlines still match; any block spanning line breaks does not
- **Fix**: add tier-2 line-ending normalisation in `internal/tools/edit/edit.go` — fold CRLF/lone CR to LF for matching, then write back in CRLF when the source file contained `\r\n`. NFC fallback now runs on the line-ending-normalised text
- **Lesson**: any exact-substring edit tool on a cross-platform codebase needs a CRLF↔LF fallback tier alongside Unicode normalisation; byte identity and visual identity diverge on Windows the moment a needle crosses a line boundary
- **Refs**: `internal/tools/edit/edit.go`, `internal/tools/edit/edit_test.go:TestEdit_CRLFFallback_*`
