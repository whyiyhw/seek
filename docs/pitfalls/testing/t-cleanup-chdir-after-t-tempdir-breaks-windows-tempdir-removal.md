### t.Cleanup(chdir) after t.TempDir() breaks Windows TempDir removal
- **Saw**: Windows CI failed with `testing.go:1369: TempDir RemoveAll cleanup: unlinkat … The process cannot access the file because it is being used by another process.` in `TestCommit_ProjectScope`.
- **Why**: `t.TempDir()` registers its cleanup via `t.Cleanup` (LIFO). When the test registered `t.Cleanup(func() { os.Chdir(cwd) })` *before* calling `t.TempDir()`, the chdir-back ran *last* — after TempDir removal had already tried (and failed on Windows) to delete the directory while CWD was still inside it. Unix allows unlinking a directory in use; Windows doesn't.
- **Fix**: call `t.TempDir()` first, then register the chdir-back `t.Cleanup` second — so the chdir-back runs first (LIFO) and restores CWD before TempDir removal. Commit `d20e446`.
- **Lesson**: combining `t.TempDir()` + `os.Chdir()`: call `t.TempDir()` *first*, then register the chdir-back `t.Cleanup`; the reverse ordering is a latent Windows-only failure. `defer os.Chdir(prev)` is safe (runs before `t.Cleanup` callbacks); `t.Cleanup` is not.
- **Refs**: `internal/tools/skillinstall/skillinstall_test.go:TestCommit_ProjectScope`
