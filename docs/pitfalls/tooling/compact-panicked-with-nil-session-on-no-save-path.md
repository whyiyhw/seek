### `/compact` panicked with nil Session on `--no-save` path
- **Saw**: running `/compact` in a session started with `--no-save` caused a nil-pointer dereference in `handleCompactDone`: `m.opts.Session.ID` evaluated before the nil guard
- **Why**: `handleCompactDone` was written assuming `m.opts.Session` is always set. On the `--no-save` path `m.opts.Session` stays `nil` by design (no persistence configured). The code tried `snapshotID = m.opts.Session.ID` outside the `if m.opts.Session != nil` guard
- **Fix**: guard the entire fork+persist block with `if m.opts.Session != nil && m.opts.Store != nil`; the notice message also branches on whether `snapshotID` is set. Commit `c3082ec`
- **Lesson**: when a struct field can be nil by design (not by bug), every method that touches it must check. Naming it `opts.Session` suggests "optional" — treat it as such everywhere, not just at startup
- **Refs**: `internal/tui/update.go:handleCompactDone`
