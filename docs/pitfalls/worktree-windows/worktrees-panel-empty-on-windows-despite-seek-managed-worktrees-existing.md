### /worktrees panel empty on Windows despite seek-managed worktrees existing
- **Saw**: TUI `/worktrees` panel always empty on Windows even when `ListFromDisk` should find entries under `~/.seek/projects/<pid>/worktrees/`
- **Why**: `git worktree list --porcelain` from Git for Windows emits forward slashes in `worktree <path>` lines, but `ListFromDisk` compared against `seekRoot` built with `filepath.Join` (backslashes). `strings.HasPrefix` is byte-exact — mixed separators never match
- **Fix**: normalize porcelain paths at parse time via `filepath.Clean(strings.ReplaceAll(p, "/", string(filepath.Separator)))` before the seekRoot prefix filter
- **Lesson**: never compare git-emitted paths with `strings.HasPrefix` against `filepath.Join` roots on Windows — normalize separators first or use `filepath.Rel`/`isPrefix`
- **Refs**: `internal/worktree/worktree.go:ListFromDisk`, `internal/worktree/worktree_test.go:TestListFromDisk_NormalizesForwardSlashes`
