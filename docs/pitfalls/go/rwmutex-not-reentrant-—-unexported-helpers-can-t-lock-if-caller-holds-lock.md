### RWMutex not reentrant — unexported helpers can't lock if caller holds lock
- **Saw**: added `p.mu.Lock()` to public methods like `Project.Add()`, which calls `p.writeEntries()` internally. If `writeEntries()` also called `p.mu.Lock()`, the goroutine would deadlock on the second acquisition
- **Why**: Go's `sync.RWMutex` is not reentrant (unlike Java's `synchronized`). A goroutine that already holds a `Lock()` and tries to `Lock()` again will block forever waiting for itself to release the first lock. Same applies to `RLock()` after `Lock()` — that's a write-lock upgrade attempt, which also deadlocks
- **Fix**: unexported helper methods (`writeEntries`, `appendArchive`) do NOT acquire locks — callers (the public methods) hold the lock for the entire duration of the operation. Documented in the `Project` type comment
- **Lesson**: when adding `sync.RWMutex` to an existing struct, audit every internal call path. The convention is: public methods lock, private helpers don't. Inline documentation ("caller must hold mu.Lock") prevents future deadlocks
- **Refs**: `internal/memory/project.go:Project`
