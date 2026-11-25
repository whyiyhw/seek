# Session Management

seek automatically saves every conversation to disk. Sessions store the full message history, token usage, model, and metadata so you can resume, fork, or review any past run.

## Storage location

Sessions are stored as JSONL files in `~/.seek/sessions/` (Linux, macOS) or `C:\Users\<user>\.seek\sessions\` (Windows). Each session is one file named by its ID.

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
| `/help`, `/?` or `?` hotkey | Show help — no arg opens a topic picker (all, commands, keys, about); pass a topic to skip the picker |
| `/clear`, `/new` | Start a fresh conversation (saves the current session, opens a new one, clears the screen). Ctrl+L if you only want to blank the visible terminal without resetting state |
| `/model` | Switch model — no arg opens a picker; pass an id to skip it |
| `/effort` | Set DeepSeek reasoning effort: `off`, `high`, or `max` |
| `/yolo` | Toggle `--yolo` (bypass permission prompts) for this session |
| `/plan` | Toggle plan mode (read-only exploration) for this session |
| `/steer`, `/s` | Mid-stream steer. With text: `/steer <text>` injects at the next turn boundary (see below); bare `/steer` promotes a queued message to a steer (Mac-friendly Alt+Enter alternative) |
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

## Injecting text mid-stream

While a turn is streaming you can type — the input box stays live. What happens to your text depends on whether the agent can take it *inside the current round*:

| Key | Behaviour |
|---|---|
| **Enter** | Injects at the next turn boundary: after the current tool batch's results land, before the model's next call. No cancellation, no lost work. |
| **Alt+Enter** (or `/steer <text>`) | Same injection first; only if the agent can't take one does it fall back to cancelling the round and resending. |
| **Enter on an empty box** | Withdraws a pending injection / queued message. |
| **Esc** | Stops everything, injections included. |

Three states are visually distinct above the input box: `⤵ inserted at next boundary` (accepted by the agent, waiting for the boundary), `↰ queued` (waiting for the whole round to end — the fallback), `↪ steering` (about to cancel the round). When an injection lands you get a `▌ you` block plus an `↪ inserted at turn boundary N` line in scrollback, so the transcript shows exactly where you cut in.

Caps: one injected message ≤ 8 KiB, at most 4 pending per turn — rejected at keystroke time rather than silently truncated, because injected text becomes permanent history and is re-sent on every later request.

### The interaction trace

Each of these interactions writes one line to `~/.seek/ui-trace.jsonl` (local only; nothing is uploaded, and `--no-save` disables it). The file records *that* you intervened and *how*, plus text length — never the text itself:

```bash
seek ui-trace                     # folded summary: intervened / delivered / withdrawn / cancelled
seek ui-trace --rows --since 24h  # raw rows, newest first
seek ui-trace --json              # machine-readable (jq-friendly)
```

`/compact` and auto-compact also write a row (`event: "compact"`). It carries the trigger in `detail` (`manual` / `auto`), the **pre-compact** prompt-token count in `tokens`, and the wall-clock wait in `dur_ms` — the folded summary reports both the token total and the duration total/average, which is what a "is auto-compact firing too early, and does it cost too much time?" pass reads.

Why a separate file rather than reading the sessions: an injected message and a steered one are byte-identical `user` rows in a transcript, and a withdrawn queue writes nothing at all. Without this ledger those three mechanisms are indistinguishable after the fact.


## Response language

seek replies in the **same language you write in** — there is no `/lang` command and no `--lang` flag. The system prompt instructs the model to match your input language (Chinese prompt → Chinese reply; English prompt → English reply).

If you need Chinese answers, write your prompts in Chinese. Scripts or habits that relied on `--lang zh` while sending English prompts must switch to Chinese input instead.

## Forking with /branch

`/branch` creates a child session that shares the same history up to the fork point. The original session is preserved unchanged. Useful for exploring two approaches from the same starting point:

```
/branch
```

seek prints the new session ID. Use `--resume <id>` to switch between branches.

## Compacting with /compact

Long sessions accumulate token cost. `/compact` summarises the history into one briefing, then continues. The pre-compact history is **not lost**: `handleCompactDone` writes the full transcript to the current session (`<old-id>`, the "snapshot") and forks a child (`<new-id>`, `parent_id` → snapshot) holding only the summary pair. `--resume <old-id>` still reaches the original.

Run `/compact` before a long task if you're near your context limit.

**Auto-compact** (`auto_compact` in `~/.seek/config.json`, or `$SEEK_AUTO_COMPACT` for one launch) runs the same thing by itself when the context crosses a configured point — `{"at_tokens": 350000}` or `{"at_fraction": 0.35}`. It fires only at rest (no queued message, no pending steer, no `/goal` loop, not after Esc), and inherits the fork-snapshot behaviour. See [guide-budget.md](guide-budget.md) §2.5.

### Measuring whether compaction helped

Three instruments, all local, none requiring a real key:

| Question | Instrument |
|---|---|
| When does it fire, and what did it cost? | `seek ui-trace` — the `compact` row carries the trigger (`manual`/`auto`) in `detail`, the pre-compact prompt tokens in `tokens`, and the wall-clock wait in `dur_ms`. The folded summary prints the token total plus duration total/average |
| Over real history: how much gets compressed, and what does the child re-read? | `scripts/transcript-stats` — its `compact 链` section builds `parent_id` chains and reports compression ratio, entity recall, and the child's first-K known re-reads against a no-compaction baseline |
| Does a *specific* summary still carry the facts a task needs? | `eval/cases/compact-ab` — same prompt over full history vs the summary pair; the scripted endpoint answers extractively, so "answered the facts" can only come from the history |

`/compact` runs only in the interactive TUI (auto-compact likewise), so `seek -p` has no compact command — that is why the eval case is built from seeded session files rather than a live compaction.

## Distilling with /distill

`/distill` scans the current session's conversation history and uses the DeepSeek reasoning model to propose ≤3 project-level decisions worth remembering. Each candidate is presented for y/n/e review before being written to project memory (M layer).

This is the primary feed mechanism for the M index — run it at the end of a session when you've made non-obvious decisions you want the agent to remember next time.

## Token accounting

The status bar shows cumulative token counts across the session:
- **prompt** — tokens sent to the model (cached + uncached)
- **hit** — tokens served from DeepSeek's prefix cache (50× cheaper: $0.006/M vs $0.30/M miss on V4.1-Flash, peak rates)
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
← {"jsonrpc":"2.0","id":2,"result":{"version":"0.12.1","model":"deepseek-flash","yolo":false}}
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
| `turn_end` | `index`, `prompt_tokens`, `completion_tokens`, `cache_hit_tokens`, `tool_calls`, `finish_reason` | LLM call settled (`finish_reason="empty_response"` = upstream returned no content; the only delta was the in-band placeholder marker) |

### Combining with --resume

```bash
seek --resume 20240522-143201-abc --rpc
```

The server loads the session history and continues from where it left off. Each `agent/prompt` call appends to the session and saves after every turn.
