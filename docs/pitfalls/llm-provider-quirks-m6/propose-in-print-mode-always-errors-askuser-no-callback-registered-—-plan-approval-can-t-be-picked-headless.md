### propose in print mode always errors "askuser: no callback registered" — plan approval can't be picked headless
- **Saw**: `--plan -p "..."` smoke: the model called `propose` in plan-analyze and got `ERROR: propose: askuser: no callback registered (programming error)`; it recovered by presenting the plan inline (exit 0, zero edits)
- **Why**: `askPolicy.SetAskFn` and the permission approval channel are registered only in `main.go`'s TUI path — the `-p`/`-json`/piped-stdin branch returns first, so `SetAskFn` never runs and asking tools (propose's approval picker, ask_user) hit `ErrNoCallback`. Provider-independent harness gap, surfaced by the GLM plan-mode smoke
- **Fix**: not fixed (out of card scope). Headless plan approval needs auto-deny + "plan printed, rerun interactively" or an approval-via-stdin callback; the inline-plan fallback is acceptable for smokes
- **Lesson**: `SetAskFn` wiring is TUI-only; any headless surface registering asking tools inherits `ErrNoCallback` — in print-mode smokes read it as "wiring gap", not model misbehaviour
- **Refs**: `cmd/seek/main.go`, `internal/askuser/askuser.go:ErrNoCallback`, `internal/tools/propose/propose.go`
