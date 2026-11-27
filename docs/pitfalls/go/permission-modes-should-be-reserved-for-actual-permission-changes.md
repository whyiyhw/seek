### Permission modes should be reserved for actual permission changes
- **Saw**: `/review` was implemented as `ModeReview` — session field, TUI cycle state, CLI flag, system-prompt suffix, status badge — yet its permission behaviour was 100% identical to `ModePlan`; only an injected review persona differed.
- **Why**: permission modes (`ModeDeny`, `ModeAsk`, `ModeYolo`, `ModePlan`) control what tools may do; mixing persona into them creates an N×M explosion — each new persona (reviewer, tester, documenter) needs a mode constant + session field + TUI state + flag + badge.
- **Fix**: `/review` is now a slash command that sets plan mode (read-only) then auto-submits a review-instruction user message; removed `ModeReview`, `Session.Review`, `Options.Review`/`SetReview`, `--review`, `reviewWorkflowReminder`, and the 4-state `cycleMode` (back to Ask→Plan→Yolo).
- **Lesson**: a new mode is warranted only when permission semantics differ; personas are shortcut commands reusing an existing mode + persona prompt, using `pkg/agent`'s `ModeLabel` per-turn reminders instead of the system prompt.
- **Refs**: this commit (downgrade /review → shortcut)
