### Trust prompt must run pre-bubbletea — askuser channel isn't drained yet
- **Saw**: the initial plan routed the shell-hooks trust dialog through `internal/askuser.Policy`, but Gate runs at main.go ~line 920 while `askPolicy.SetAskFn` gets a real callback only at ~line 1145 — and that callback pushes onto a channel the TUI bubbletea loop is supposed to drain, which hasn't started; any pre-TUI askuser call blocks forever.
- **Why**: askuser is a TUI-coupled async primitive by design — the SetAskFn callback expects a goroutine on the other end; there is no version that works before `tea.NewProgram(...).Run()` has spun up.
- **Fix**: a separate `stdinTrustPrompt` in `cmd/seek/hooks_trust.go` reads stdin directly when isatty=true and refuses with a warning when not — piped / non-TTY launches auto-refuse rather than hang.
- **Lesson**: not every "ask the user" needs askuser.Policy — pre-bubbletea questions belong on stdin, post-bubbletea questions in the policy; two paths cost one ~80-line file, unifying them costs reworking the TUI start order.
- **Refs**: `cmd/seek/hooks_trust.go`, `internal/askuser/askuser.go` (the policy deliberately NOT used here)
