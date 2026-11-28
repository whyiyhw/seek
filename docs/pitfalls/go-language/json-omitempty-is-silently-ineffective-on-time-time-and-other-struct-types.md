### `json:",omitempty"` is silently ineffective on `time.Time` and other struct types
- **Saw**: added `StaleSince time.Time` with `,omitempty` to `internal/memory.Entry`; every fresh entry's memory.jsonl line still carried `"stale_since": "0001-01-01T00:00:00Z"` — the tag did nothing
- **Why**: `encoding/json`'s omitempty checks the Go *zero value* of a few specific kinds (false, 0, "", nil, empty array/map/slice). For structs like `time.Time` the zero value is a *struct*, not any of those — omitempty never fires and the struct is always emitted. `time.Time{}.IsZero()` is true at runtime, but encoding/json doesn't call IsZero
- **Fix**: switched to `json:",omitzero"` (Go 1.24+): omitzero checks IsZero() on types that implement it, including time.Time. go.mod is already 1.25.x, so it's free
- **Lesson**: omitempty on `time.Time` or any struct field is a no-op. Use `,omitzero` (Go 1.24+), or a `*time.Time` if you need backwards-compatible behaviour. Eyeball the JSON file after each schema change — catches this fast
- **Refs**: `internal/memory/memory.go:Entry.StaleSince`
