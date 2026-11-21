<!-- pitfalls-absorbed: original-section: - -->
### [已吸收] Explicit `/lang` / `--lang` removed — reply language follows user input
- **Saw**: users expected `/lang zh` or `--lang zh` to force Chinese replies while typing English prompts; docs and examples still listed the command after removal
- **Why**: product decision — match-user-input is simpler, avoids fighting prefix cache with per-turn language suffixes, and matches how most users already work (write Chinese → get Chinese). Forcing locale or a slash command added state (`Session.Lang`, `Agent.SetLang`, OS locale detection) that duplicated the system prompt
- **Fix**: system prompt now starts with `Language: Match the user's input language…`; removed `--lang`, `/lang`, `Session.Lang`, `Agent.SetLang`, and `langReminder`. Synced `docs/guide-sessions.md`, `docs/comparison.md`, `examples/`, and this log. Commit (pending)
- **Lesson**: if the model should speak language X, the durable fix is "user writes in X" — not a parallel config axis. Breaking removals need the same doc sweep as the code deletion (examples HTML, comparison tables, pitfalls cross-refs)
- **Refs**: `cmd/seek/main.go:systemPromptTpl`, `docs/guide-sessions.md`
- **已吸收**: `/lang` was re-introduced by the i18n feature as a view-layer-only switch (persists to ~/.seek/config.json, SEEK_LANG overrides); the "reply language follows user input" half remains current design → `docs/prd/feature-i18n.md`; `internal/tui/commands.go` "/lang [en|zh]"
