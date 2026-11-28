### Permission modes should be reserved for actual permission changes
- **Saw**: `/review` shipped as `ModeReview` — session field, TUI cycle state, CLI flag, system-prompt suffix, status badge — yet its permission behaviour was 100% identical to `ModePlan`; only the injected persona differed.
- **Why**: permission modes (`ModeDeny`, `ModeAsk`, `ModeYolo`, `ModePlan`) control what tools may do; mixing persona in creates an N×M explosion — each new persona (reviewer, tester, documenter) needs a mode constant + session field + TUI state + flag + badge.
- **Fix**: `/review` is now a slash command that sets plan mode (read-only) then auto-submits a review instruction; removed `ModeReview`, `Session.Review`, `Options.Review`/`SetReview`, `--review`, `reviewWorkflowReminder`, and the 4-state `cycleMode` (back to Ask→Plan→Yolo).
- **Lesson**: a new mode is warranted only when permission semantics differ; personas are shortcuts reusing an existing mode + persona prompt, with `pkg/agent`'s `ModeLabel` per-turn reminders instead of the system prompt.
- **Refs**: this commit (/review → shortcut)
