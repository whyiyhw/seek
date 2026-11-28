### skill_commit must not silently pick a scope — the model has no signal for user intent
- **Saw**: `skill_commit` defaulted to user scope (`~/.seek/skills/`): "install this skill from GitHub" could silently land in `$HOME` when the user wanted the project repo, or vice versa.
- **Why**: user scope is private to the machine, project scope is shared via git — neither tool name nor source URL tells the model the user's intent, and hard-coding `user` meant it never learned to ask.
- **Fix**: `scope` is a required enum (`"user"` | `"project"`), schema-rejected when omitted; system prompt + tool description say to ask first, the approval prompt shows the resolved target path, tests cover missing/invalid/both valid scopes. Commit `d86d509`.
- **Lesson**: a parameter encoding a high-consequence user choice (private vs shared, local vs remote) must be a required enum, not a silent default — the model can't infer intent for filesystem-layout or visibility decisions; such an "A or B" is an `ask_user` candidate.
- **Refs**: `internal/tools/skillinstall/skillinstall.go`, `cmd/seek/main.go`, `docs/guide-skills.md`, commit `d86d509`
