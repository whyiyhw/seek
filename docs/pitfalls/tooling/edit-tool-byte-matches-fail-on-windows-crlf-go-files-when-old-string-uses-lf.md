### edit tool byte-matches fail on Windows CRLF Go files when old_string uses LF
- **Saw**: on Windows, `edit` with a multi-line `old_string` copied from model output (LF) against a CRLF Go file always fails with `expected 1 replacements but old_string occurs 0 times`, though the visible text is identical; agents fall back to Python/PowerShell one-off scripts
- **Why**: `edit` tier-1 matching is exact bytes. Windows Go files default to `\r\n`; model-produced needles use `\n`. Single-line edits without embedded newlines still match; blocks spanning line breaks do not
- **Fix**: add tier-2 line-ending normalisation in `internal/tools/edit/edit.go`: fold CRLF/lone CR to LF for matching, write back as CRLF when the source contained `\r\n`. The NFC fallback runs on that normalised text
- **Lesson**: any exact-substring edit tool on a cross-platform codebase needs a CRLF↔LF fallback tier alongside Unicode normalisation; byte and visual identity diverge on Windows once a needle crosses a line boundary
- **Refs**: `internal/tools/edit/edit.go`, `internal/tools/edit/edit_test.go:TestEdit_CRLFFallback_*`
