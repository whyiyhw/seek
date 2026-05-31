# seek 命令参考 / Commands Reference

All slash commands, key bindings, and CLI flags.

---

## 1. TUI 斜杠命令 / Slash Commands

| Command | Alias | Usage |
|---------|-------|-------|
| `/help` | `/help`, `/?` | Show help topic picker; pass a topic (all, commands, keys, about) |
| `/hooks` | | List active shell hooks (user + project) |
| `/clear` | `/new` | Start a fresh conversation (saves current session, opens new one, clears screen). Ctrl+L clears display only |
| `/model` | `/model [id]` | Switch model — no arg opens a picker |
| `/effort` | `/effort [off\|high\|max]` | Set DeepSeek reasoning effort |
| `/yolo` | | Toggle --yolo (bypass permission prompts) |
| `/plan` | | Toggle plan mode (read-only exploration → propose → execute) |
| `/review` | `/review [branch]` | Code review working-tree changes (shorthand for /code-review quick) |
| `/code-review` | `/code-review [quick\|thorough] [--fix] [--comment] [branch]` | Code-review at chosen effort level; --fix proposes fixes, --comment posts inline PR comments |
| `/branch` | | Fork this session (new ID, parent link) |
| `/compact` | | Summarise prior history to free up context |
| `/checkpoints` | | List git checkpoints for current session |
| `/distill` | | Extract project decisions → M memory |
| `/skills` | | List loaded skills with source paths |
| `/skill` | `/skill <verb> [args]` | Manage skill packages (install, uninstall, list, …) |
| `/memory` | `/memory <verb> [args]` | Inspect project memory (list, show, search, archive) |
| `/setup` | | Re-run the API-key configuration wizard |
| `/upgrade` | `/upgrade [--force] [--dry-run]` | Download latest release and replace binary |
| `/exit` | `/exit`, `/quit`, `/q` | Quit seek |
| `/steer` | `/steer`, `/s` | Interrupt and send new instructions |
| `/undo` | `/undo [path]` | Undo the last write or edit |
| `/redo` | `/redo [path]` | Redo a previously undone write or edit |
| `/restore` | `/restore [turn]` | Restore entire working tree from a git checkpoint |
| `/goal` | `/goal [<condition> \| clear]` | Work autonomously until a condition is met. No args shows status; `clear` stops |
| `/agents` | | List subagents for this project (spawn time, type, status, turns, tokens, description) |
| `/worktrees` | | List seek-managed git worktrees (id, branch, path) |
| `/diagnose` | | Print a diagnostic report (version, OS, provider, config, session metadata) |
| `/keys` | | Show the active keymap (incl. any ~/.seek/keybindings.toml overrides) |

---

## 2. 关键绑定 / Key Bindings

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Shift+Enter` | New line (in textarea) |
| `Esc` | Cancel current response / interrupt generation |
| `Ctrl+C` | Quit seek |
| `Ctrl+L` | Clear screen (display only, keeps session) |
| `Up/Down` | Navigate history / picker items |
| `Tab` | Auto-complete file paths in textarea |
| `?` | Open help overlay (when not in textarea) |

---

## 3. CLI 参数 / CLI Flags

```bash
seek                        # Start TUI (interactive)
seek -p "question"          # One-shot prompt (non-interactive)
seek -json                  # JSON output mode (for piping)
seek --rpc                  # JSON-RPC 2.0 server mode (IDE integration)
seek --resume <id>          # Resume a saved session by ID (see seek -list)
seek --continue             # Resume the most-recently-updated session
seek --model <name>         # Override model selection
seek --provider <name>      # Override provider (deepseek/anthropic/openai/gemini)
seek --yolo                 # Bypass permission prompts
seek --plan                 # Start in plan mode
seek --no-save              # Ephemeral session (no persistence)
seek --keep-checkpoints     # Preserve checkpoint blobs across session end
seek --install              # Add seek to PATH (Windows)
seek --upgrade              # Download latest release and replace binary
seek --version              # Print version and exit
```

### Subcommands

```bash
seek goal run "<condition>"       # Unattended multi-turn task
seek autopilot run "<goal>"        # Multi-agent parallel orchestration
seek cron create ...               # Schedule a recurring job
seek cron list                     # List scheduled jobs
seek cron tick                     # Manually trigger due jobs
seek skill install <path|url>      # Install a skill
seek skill list                    # List installed skills
seek checkpoint list               # List git checkpoints
seek checkpoint restore <turn>     # Restore working tree
seek undo                          # Undo last file write/edit (CLI)
seek redo                          # Redo last undone change (CLI)
seek memory list                   # List project memory entries
seek memory search <query>         # Search memory entries
seek hooks list                    # List trusted hook scripts
seek hooks trust <path>            # Trust a hook script
seek keys list                     # Show active keybindings
seek worktree gc                   # Garbage-collect stale worktrees
```

---

## 4. 环境变量 / Environment Variables

| Variable | Purpose |
|----------|---------|
| `DEEPSEEK_API_KEY` | DeepSeek API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `OPENAI_API_KEY` | OpenAI API key |
| `GEMINI_API_KEY` | Gemini API key |
| `SEEK_HOME` | Override `~/.seek/` location |
| `SEEK_WEBFETCH_ALLOW_HTTP=1` | Allow http:// URLs in webfetch (dev only) |
