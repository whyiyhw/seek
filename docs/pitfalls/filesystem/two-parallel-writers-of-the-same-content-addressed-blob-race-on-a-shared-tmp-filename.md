### Two parallel writers of the SAME content-addressed blob race on a shared `.tmp` filename
- **Saw**: checkpoint's concurrent-snapshot test failed with ~half the expected events; warnings logged "rename ... .tmp ... : no such file or directory" for many goroutines.
- **Why**: `storeBlobLocked(sha, content)` used a fixed `<bp>.tmp` then `os.Rename(tmp, bp)`; two goroutines hashing the same content each Stat-miss, WriteFile(tmp), Rename — the winning rename erases the .tmp path, so the loser's WriteFile succeeds but its rename fails (the path is gone).
- **Fix**: swap fixed-name `<bp>.tmp` for `os.CreateTemp(dir, base+".tmp.*")` so every writer gets a unique scratch path, then absorb the "target already exists" race after `Rename` errors by checking `Stat(bp)` and dropping our tmp copy. Commit (M9.1).
- **Lesson**: content-addressed storage is forgiving about losing the race (the winner's bytes equal yours) but unforgiving about sharing a `.tmp` filename — use a unique tmp name per writer OR a per-blob mutex; unique-tmp is simpler and lock-free.
- **Refs**: `internal/checkpoint/file.go:storeBlobLocked`, `internal/checkpoint/checkpoint_test.go:TestFile_ConcurrentSnapshots`
