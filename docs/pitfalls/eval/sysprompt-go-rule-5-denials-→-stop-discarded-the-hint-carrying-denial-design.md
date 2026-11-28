### `sysprompt.go` rule 5 ("denials → stop") discarded the Hint-carrying denial design
- **Saw**: policy denials embedding a `Hint:` clause (drop a `cd` prefix, use the `git` tool instead of bash git, `go test`→`go vet`) got a full stop: system-prompt workflow rule 5 says "stop — do not loop"; AGENTS.md's "Self-recovery first" and `plan-mode.md`'s "read it and adjust" say the opposite.
- **Why**: rule 5 predates the bash `Hint:` mechanism (`internal/tools/bash/hint.go`) and was never revisited; the system prompt is re-sent every turn, closest to generation, so its "stop" won.
- **Fix**: rule 5 now splits by source — user-declined y/N → stop, never re-ask; policy denial with a `Hint:` → adjust once (different tool or corrected shape); still denied → surface and stop. No test change: `internal/sysprompt`'s oracle reuses `rootTpl`. (this change; commit pending)
- **Lesson**: when an error gains machine-actionable metadata (hints, alternatives), grep instruction surfaces for blanket "on error: stop" rules — they silently discard it.
- **Refs**: `internal/sysprompt/sysprompt.go`, `internal/tools/bash/hint.go`, `internal/skill/builtin/plan-mode.md` "Common bash pitfalls in plan-analyze"
