### `<project>/.seek/project-id` is per-machine state — must be gitignored
- **Saw**: `git status` in seek's own repo showed `.seek/` untracked. If committed, every clone would pick up the original author's `sha256("/Users/alice/code/seek")[:16]` as project-id and route their M under `~/.seek/projects/<alice's hash>/` — not data-corrupting (each user's M stays under their own home), but the dir name loses all relation to any local path.
- **Why**: `LoadOrCreate` writes `.seek/project-id` best-effort on every run to support single-user "project moved" recovery (the pointer travels with the tree so `mv` keeps M history); that use case is per-user, cross-user propagation never intended.
- **Fix**: `.seek/project-id` added to repo `.gitignore`. Pattern is narrow (just the file) rather than `.seek/` because `<project>/.seek/skills/` is meant to be team-shared.
- **Lesson**: auto-generated per-machine pointer files MUST be gitignored at creation — "we'll gitignore it later" is exactly when it slips into a `git add .`; a best-effort first-run file needs its gitignore entry in the same commit.
- **Refs**: `internal/memory/project.go:writeProjectPointer`, `.gitignore`
