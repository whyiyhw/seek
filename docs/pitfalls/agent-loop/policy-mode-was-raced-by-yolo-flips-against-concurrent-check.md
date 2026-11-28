### `Policy.mode` was raced by /yolo flips against concurrent `Check`
- **Saw**: a `-race` test on concurrent Check + SetMode lit up four data-race warnings on the `Policy.mode` field. In production this could fire when the user pressed `/yolo` while a tool dispatch was in flight (TUI goroutine writes mode, agent goroutine reads it via Check)
- **Why**: the Policy struct held `mode` and `askFn` as plain fields with no synchronization; the bug never manifested because tool dispatch is sequential and `/yolo` between dispatches was the common case — but `-race` catches the *contract* violation regardless of timing luck
- **Fix**: `sync.RWMutex` on Policy; Check snapshots mode/askFn/cwd under RLock and releases BEFORE calling askFn (which blocks on the user for arbitrary time). Commit `73c5f3d`
- **Lesson**: a permission gate is a synchronization primitive whether you designed it as one or not. Any field mutable post-construction needs the mutex. Found by writing a `-race` test for the concurrent path — the discipline AGENTS.md now requires
- **Refs**: `internal/permission/permission.go:Policy`; test `TestCheck_ConcurrentCallsRaceFree`
