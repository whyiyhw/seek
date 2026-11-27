### Word-level vs character-level similarity for LLM-produced trait merging
- **Saw**: Levenshtein character distance (≥0.75) merged "low evidence trait" with "high evidence trait" (0.84, 3 edits/19 chars) while "prefers explicit error handling over panic" vs "prefers explicit error handling" scored 0.74 and stayed apart.
- **Why**: short trait descriptions share template substrings ("evidence trait") and differ on the preference word, but character distance is blind to word boundaries — 3 edits of 19 look similar, while 11 added chars on a 42-char base fall below the threshold.
- **Fix**: Jaccard word-overlap similarity (≥0.55), words split on spaces; for CJK (no spaces) fall back to Levenshtein character ratio, since CJK characters carry semantic weight individually.
- **Lesson**: no single metric fits multi-word English and CJK — word-level for space-delimited languages, character-level for scripts without word boundaries; test border cases on both.
- **Refs**: `internal/memory/merge.go:traitSimilarity`
