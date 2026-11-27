### A tool result appended on the success path vanishes whenever the tool also returns an error
- **Saw**: the repeat-call reminder was attached to the returned result string — it fired in tests returning `(result, nil)` and never reached the model in the case it was built for: a failing command re-run six times.
- **Why**: `buildToolResultMsg` documents "errors always win" — when `terr != nil` the content becomes `fmt.Sprintf("tool error: %v", terr)`, discarding the result string produced alongside it.
- **Fix**: `appendReminder` attaches to whichever channel the model sees — `result + note` when err is nil, `fmt.Errorf("%w\n\n%s", err, note)` otherwise; `%w` keeps `errors.Is` working (`permission.ErrDenied` is checked) and the note goes after the original text so prefix parsing still matches.
- **Lesson**: a tool's error and result are one channel with the error taking precedence — anything advisory must ride on both, and appending to a wrapped error is the only way to survive the error path.
- **Refs**: `internal/tools/repeatguard.go:appendReminder`, `internal/tools/repeatguard_test.go:TestRepeatGuard_ReminderSurvivesOnErrorPath`, `pkg/agent/agent.go:buildToolResultMsg`
