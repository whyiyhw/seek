### Top-level `var` slice and `func` that reference each other → init cycle
- **Saw**: `internal/tui/commands.go` failed `go vet` with `initialization cycle for commands` after I made cmdHelp read from a top-level `commands` slice
- **Why**: `var commands = []command{ {handler: cmdHelp}, ... }` references `cmdHelp` at init time; `cmdHelp` reads `commands` — Go's initialiser can't sequence both
- **Fix**: turned `commands` into `func allCommands() []command { return ... }` so the slice is built lazily on first call. Commit `08449cd`
- **Lesson**: when a top-level `var` and the functions it lists reference each other, demote the var to a func. Initialisation order isn't worth fighting
