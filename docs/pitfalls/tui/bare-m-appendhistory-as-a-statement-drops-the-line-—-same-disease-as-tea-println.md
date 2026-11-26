### Bare `m.appendHistory(...)` as a statement drops the line — same disease as `_ = tea.Println`
- **Saw**: /distill's skill-segment rejection lines ("· skill candidate rejected — …") never appeared in scrollback despite the handler calling appendHistory for each — surfaced by `/distill --dry-run`, whose real-run half of the byte-diff was empty.
- **Why**: `appendHistory` doesn't buffer — it RETURNS the `tea.Println` cmd that prints, and the rejection loop called it as a bare statement inside a `[]tea.Cmd`-returning handler, so the cmds fell on the floor (compiles clean, vet silent).
- **Fix**: collect and return the cmds (`cmds = append(cmds, m.appendHistory(...))`), pinned by `TestHandleSkillDistillDone_RejectionCmdsAreReturned`.
- **Lesson**: in a `[]tea.Cmd`-returning handler every appendHistory call must be wired into the returned slice — assert on the returned cmds' count, not just model state.
- **Refs**: `internal/tui/distill_skill.go:handleSkillDistillDone`, `internal/tui/model.go:appendHistory`, `internal/tui/distill_skill_dryrun_test.go`、docs/book/chapter-17.md
