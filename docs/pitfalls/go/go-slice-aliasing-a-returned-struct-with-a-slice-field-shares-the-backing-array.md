### Go slice aliasing: a returned struct with a slice field shares the backing array
- **Saw**: `Project.Entries()` returned `[]Entry` where each `Entry` had a `Tags []string`. A caller's `entries[0].Tags = append(entries[0].Tags, "x")` could silently mutate the project's internal entry map (spare capacity) or appear to work (none) — non-deterministic behaviour
- **Why**: a slice header is a (ptr, len, cap) triplet: copying an `Entry` copies the `Tags` header, which still points at the same array. `append` with spare capacity writes into the shared array; without it, allocates. The result depends on capacity at call time
- **Fix**: added defensive `make+copy` in both `Entries()` (on read-out) and `Add()` (on store-in). Three lines each, zero runtime cost for entries without Tags
- **Lesson**: any exported method returning a struct with a slice, or storing one passed in, must defensively copy it: the bug is invisible until a caller mutates, then explodes
- **Refs**: `internal/memory/project.go:Entries`, `project.go:Add`
