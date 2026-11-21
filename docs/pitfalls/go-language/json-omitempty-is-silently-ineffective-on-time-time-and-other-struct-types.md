### `json:",omitempty"` is silently ineffective on `time.Time` and other struct types
- **Saw**: added `StaleSince time.Time` with `,omitempty` to `internal/memory.Entry`. Every fresh entry's memory.jsonl line still contained `"stale_since": "0001-01-01T00:00:00Z"` — the omitempty tag did nothing
- **Why**: `encoding/json`'s omitempty checks for the Go *zero value* of a few specific kinds (false, 0, "", nil, empty array/map/slice). For struct values like `time.Time`, the zero value is a *struct*, not any of those — so omitempty never fires and the (non-zero-bit-pattern) struct is always emitted. `time.Time{}.IsZero()` returns true at runtime, but encoding/json doesn't call IsZero
- **Fix**: switched to `json:",omitzero"`, added in Go 1.24+. omitzero specifically checks IsZero() for types that implement it, including time.Time. Project's go.mod is already 1.25.x so this is free
- **Lesson**: omitempty on `time.Time` or any struct field is a no-op. Use `,omitzero` (Go 1.24+), or `*time.Time` pointer if you need backwards-compatible behaviour. Eyeballing a JSON file after every schema change catches this fast
- **Refs**: `internal/memory/memory.go:Entry.StaleSince`
