# Session Management

seek automatically saves every conversation to disk. Sessions store the full message history, token usage, model, and metadata so you can resume, fork, or review any past run.

## Storage location

Sessions are stored as JSONL files in `~/.local/share/seek/sessions/` (Linux/macOS) or `%LOCALAPPDATA%\seek\sessions\` (Windows). Each session is one file named by its ID.

## Basic operations

```bash
# List saved sessions (most recent first)
seek --list

# Resume the most recently updated session
seek --continue

# Resume a specific session by ID
seek --resume 20240522-143201-abc

# Skip saving entirely (ephemeral session)
seek --no-save -p "scratch work"
```

## In-session commands

| Command | What it does |
|---|---|
| `/help`, `/?` | Show this help |
| `/clear` | Clear visible screen (scrollback preserved by terminal) |
| `/new` | Start a fresh conversation (saves the current one first) |
| `/model` | Switch model — no arg opens a picker; pass an id to skip it |
| `/effort` | Set DeepSeek reasoning effort: `off`, `high`, or `max` |
| `/lang` | Set response language: `en`, `zh`, or `auto` |
| `/yolo` | Toggle `--yolo` (bypass permission prompts) for this session |
| `/plan` | Toggle plan mode (read-only exploration) for this session |
| `/review` | Code review working-tree changes — no arg opens a picker, or pass a branch |
| `/branch` | Fork this session — new ID, parent link, copy of history |
| `/compact` | Summarise prior history into one message to free up context |
| `/distill` | Extract project-level decisions from this session into M memory |
| `/skills` | List loaded skills with source paths |
| `/skill` | Manage skill packages: `install`, `uninstall`, `update`, `list`, `status`, `stats`, `help` |
| `/memory` | Inspect project memory: `list`, `show`, `search`, `archive` |
| `/setup` | Re-run the API-key configuration wizard |
| `/upgrade` | Download the latest release and replace the binary in place |
| `/exit`, `/quit`, `/q` | Quit seek |

## Forking with /branch

`/branch` creates a child session that shares the same history up to the fork point. The original session is preserved unchanged. Useful for exploring two approaches from the same starting point:

```
/branch
```

seek prints the new session ID. Use `--resume <id>` to switch between branches.

## Compacting with /compact

Long sessions accumulate token cost. `/compact` summarises the older portion of the history into a single assistant message, then continues. The summary is appended to the session — the original turns are gone after a save.

Run `/compact` before a long task if you're near your context limit.

## Distilling with /distill

`/distill` scans the current session's conversation history and uses the DeepSeek reasoning model to propose ≤3 project-level decisions worth remembering. Each candidate is presented for y/n/e review before being written to project memory (M layer).

This is the primary feed mechanism for the M index — run it at the end of a session when you've made non-obvious decisions you want the agent to remember next time.

## Token accounting

The status bar shows cumulative token counts across the session:
- **prompt** — tokens sent to the model (cached + uncached)
- **hit** — tokens served from DeepSeek's prefix cache (~50× cheaper: $0.0028/M vs $0.14/M miss on V4-Flash)
- **gen** — generated completion tokens

## RPC mode (for IDE integrations)

`seek --rpc` starts a JSON-RPC 2.0 server over stdin/stdout. Host processes (IDE extensions, scripts) send requests and receive streaming responses without a terminal.

```bash
seek --rpc
```

On stderr: `seek rpc: listening on stdin (JSON-RPC 2.0)`

### Protocol

All messages are JSON objects, one per line. Requests follow JSON-RPC 2.0.

**Methods:**

`agent/prompt` — run a prompt; streams events as notifications while processing.

```json
→ {"jsonrpc":"2.0","id":1,"method":"agent/prompt","params":{"text":"explain main.go"}}
← {"jsonrpc":"2.0","method":"agent/event","params":{"type":"turn_start","index":0}}
← {"jsonrpc":"2.0","method":"agent/event","params":{"type":"text_delta","delta":"main.go is…"}}
← {"jsonrpc":"2.0","method":"agent/event","params":{"type":"turn_end","index":0,"prompt_tokens":120,"completion_tokens":45}}
← {"jsonrpc":"2.0","id":1,"result":{"turns":1,"tool_calls":0,"prompt_tokens":120,"completion_tokens":45,"cache_hit_tokens":0}}
```

`agent/info` — return server capabilities.

```json
→ {"jsonrpc":"2.0","id":2,"method":"agent/info"}
← {"jsonrpc":"2.0","id":2,"result":{"version":"0.9.0","model":"deepseek-chat","yolo":false}}
```

`session/list` — list saved sessions.

```json
→ {"jsonrpc":"2.0","id":3,"method":"session/list"}
← {"jsonrpc":"2.0","id":3,"result":{"sessions":[{"id":"...","model":"deepseek-chat","turns":5,"tool_calls":12}]}}
```

### Event types (`agent/event` notification params)

| `type` | Extra fields | Description |
|---|---|---|
| `turn_start` | `index` | LLM call begins |
| `text_delta` | `delta` | Incremental assistant text |
| `reasoning_delta` | `delta` | Chain-of-thought from V4 reasoning mode / `Thinking.Type=enabled` (dimmed in TUI) |
| `tool_start` | `id`, `name`, `args` | Tool about to execute |
| `tool_delta` | `id`, `name`, `delta` | Intermediate output from streaming tool |
| `tool_end` | `id`, `name`, `result`/`error`, `bytes` | Tool finished |
| `turn_end` | `index`, `prompt_tokens`, `completion_tokens`, `cache_hit_tokens`, `tool_calls` | LLM call settled |

### Combining with --resume

```bash
seek --resume 20240522-143201-abc --rpc
```

The server loads the session history and continues from where it left off. Each `agent/prompt` call appends to the session and saves after every turn.
