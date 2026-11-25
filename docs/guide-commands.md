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
| `/effort` | `/effort [off\|high\|max]` | Set DeepSeek reasoning effort; under the default `--max-tokens`, `max` also raises the per-call completion cap to 64k, clamped to the model's own output ceiling |
| `/yolo` | | Toggle --yolo (bypass permission prompts) |
| `/plan` | | Toggle plan mode (read-only exploration → propose → execute) |
| `/review` | `/review [branch]` | Code review working-tree changes (shorthand for /code-review quick) |
| `/code-review` | `/code-review [quick\|thorough] [--fix] [--comment] [branch]` | Code-review at chosen effort level; --fix proposes fixes, --comment posts inline PR comments |
| `/branch` | | Fork this session (new ID, parent link) |
| `/compact` | | Summarise prior history to free up context |
| `/checkpoints` | | List git checkpoints for current session |
| `/distill` | | /distill [--dry-run] — extract project decisions into M memory (per-candidate y/n/e); --dry-run previews the skill-draft funnel, nothing written |
| `/skills` | | List loaded skills with source paths |
| `/skill` | `/skill <verb> [args]` | Manage skill packages (install, uninstall, list, drafts, …) |
| `/memory` | `/memory <verb> [args]` | Inspect project memory (list, show, search, archive) |
| `/setup` | | Re-run the API-key configuration wizard |
| `/upgrade` | `/upgrade [--force] [--dry-run]` | Download latest release and replace binary |
| `/exit` | `/exit`, `/quit`, `/q` | Quit seek |
| `/steer` | `/steer`, `/s` | Interrupt and send new instructions |
| `/undo` | `/undo [path]` | Undo the last write or edit |
| `/redo` | `/redo [path]` | Redo a previously undone write or edit |
| `/restore` | `/restore [turn]` | Restore entire working tree from a git checkpoint |
| `/goal` | `/goal [<condition> \| clear]` | Work autonomously until a condition is met. No args shows status; `clear` stops |
| `/agents` | `/agents [--all\|types]` | List subagents for this project (spawn time, type, status, turns, tokens, description); `types` lists defined agent types (built-in + `.seek/agents`) |
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
seek --rpc                  # JSON-RPC 2.0 server mode (frozen legacy: 3 methods; for IDE use `seek acp` — see guide-zed.md; for data queries use `seek serve` /rpc)
seek --resume <id>          # Resume a saved session by ID (see seek -list)
seek --continue             # Resume the most-recently-updated session
seek --model <name>         # Override model selection
seek --provider <name>      # Override provider (deepseek/anthropic/openai/gemini/glm)
seek --effort off|high|max   # Reasoning effort for this session (default: max). `off` = no reasoning_effort override and the 16k completion cap; `max` raises the derived cap to 64k (clamped to the model). An explicit --max-tokens still wins
seek --temperature <0..2>   # Sampling temperature (absent = provider default, NO key on the wire). 0 is the determinism pin the judge lane wants
seek --yolo                 # Bypass permission prompts
seek --plan                 # Start in plan mode
seek --no-save              # Ephemeral session (no persistence)
seek --keep-checkpoints     # Preserve checkpoint blobs across session end
seek --install              # Add seek to PATH (Windows)
seek --upgrade              # Download latest release and replace binary
seek --version              # Print version and exit
```

### `--json` 事件流契约 / `--json` event stream contract

`seek -p … --json` writes one JSON object per line to stdout (JSONL; human-readable diagnostics go to stderr). Event types are a stable contract — adding a type or a field is a minor change, removing or repurposing one is a major break.

| `type` | Extra fields | Meaning |
|---|---|---|
| `agent_start` | `model` | Run begins; the model bound to it |
| `turn_start` | `index` | One LLM call begins |
| `text_delta` / `reasoning_delta` | `delta` | Incremental assistant text (`reasoning_delta` = CoT from thinking-mode responses) |
| `tool_start` / `tool_delta` / `tool_end` | `id`, `name`, `args` / `delta` / `result` \| `error`, `bytes` | Tool lifecycle |
| `turn_end` | `index`, `prompt_tokens`, `completion_tokens`, `cache_hit_tokens`, `tool_calls`, `finish_reason` | One LLM call settled |
| `agent_end` | `turns`, `tool_calls`, token totals, `session_id` | Run complete |
| `error` | `error` | Fatal error |
| `approval_request` / `approval_cancel` | `req_id`, `kind`, `summary`, `command`, `diff` | `--approval-stdin` only |

**Empty completions are a machine-checkable signal, not prose.** When the upstream burns its whole completion budget without producing content (e.g. reasoning-only, reproducible with a small `--max-tokens`), seek self-heals: it books an in-band placeholder marker as the assistant text and keeps the run alive. That marker (`[empty: …]`) also rides the stream as an ordinary `text_delta`, so a consumer that just concatenates deltas would report it as the answer. Branch on `turn_end.finish_reason` instead:

- normally the upstream finish reason (`stop`, `tool_calls`, `length`, `refusal`, …);
- **`empty_response`** — the turn produced no content; any `text_delta` on that turn is the placeholder marker, not an answer.

`finish_reason` is optional (`omitempty`): on lines that carry no classification the key is absent, so existing parsers are unaffected.

```bash
seek -p '…' --json | jq -c 'select(.type=="turn_end" and .finish_reason=="empty_response")'
```

### Subcommands

```bash
seek goal run "<condition>"       # Unattended multi-turn task
seek autopilot run "<goal>"        # Multi-agent parallel orchestration
seek serve                         # Remote control plane (--frontend=web ships)
seek acp                           # Agent Client Protocol server (Zed, …)
seek cron create ...               # Schedule a recurring job
seek cron list                     # List scheduled jobs
seek cron tick                     # Manually trigger due jobs
seek skill install <path|url>      # Install a skill
seek skill list                    # List installed skills
seek skill drafts                  # Review distilled skill drafts (list/preview/approve)
seek checkpoint list               # List git checkpoints
seek checkpoint restore <turn>     # Restore working tree
seek undo                          # Undo last file write/edit (CLI)
seek redo                          # Redo last undone change (CLI)
seek memory list                   # List project memory entries
seek memory search <query>         # Search memory entries
seek hooks list                    # List trusted hook scripts
seek hooks trust <path>            # Trust a hook script
seek ui-trace                      # Folded summary of local interaction trace (~/.seek/ui-trace.jsonl)
seek ui-trace --rows --since 24h   # Raw rows (newest first) from the last day
seek keys list                     # Show active keybindings
seek worktree gc                   # Garbage-collect stale worktrees
seek task run "<command>"          # Start a task that outlives this seek process
seek ps                            # List task processes (alias: seek task ps)
seek attach <id>                   # Follow a task's output until it ends
seek kill <id>                     # SIGTERM a task's process group, then SIGKILL
```

---

## 4. 环境变量 / Environment Variables

| Variable | Purpose |
|----------|---------|
| `DEEPSEEK_API_KEY` | DeepSeek API key |
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `OPENAI_API_KEY` | OpenAI API key |
| `GEMINI_API_KEY` | Gemini API key |
| `GLM_API_KEY` | Zhipu bigmodel.cn API key (glm provider) |
| `GLM_BASE_URL` | GLM endpoint override — Coding Plan subscriptions must use `https://open.bigmodel.cn/api/coding/paas/v4/chat/completions` (see [guide-config.md](guide-config.md) §3) |
| `SEEK_HOME` | Override `~/.seek/` location |
| `SEEK_WEBFETCH_ALLOW_HTTP=1` | Allow http:// URLs in webfetch (dev only) |
