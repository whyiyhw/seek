### `/model` updated the display but not the Agent's model — API calls still used the old model
- **Saw**: `/model deepseek-v4-pro` updated the status bar but DeepSeek's backend stats showed all calls as `deepseek-v4-flash` — the switch had zero effect on API requests.
- **Why**: `cmdModel` / `applyModelChoice` only updated `m.opts.Model` and called `SetModel(args)` (sessionModel in main.go); the Agent's `a.cfg.Model` is frozen at `agent.New` and `RebuildAgent` only runs on `/new`, so every call kept the startup model.
- **Fix**: added `Agent.SetModel(model)` in `pkg/agent/agent.go` mutating `a.cfg.Model` in place (safe between turns); `cmdModel` and `applyModelChoice` now call `m.opts.Agent.SetModel(args)` after the display update.
- **Lesson**: a struct whose config is baked at construction and read on every operation must expose a mutator if a command claims to change it at runtime — "effective on next prompt" requires the agent's copy changed.
- **Refs**: `pkg/agent/agent.go:SetModel`, `internal/tui/commands.go:cmdModel`, `internal/tui/commands.go:applyModelChoice`
