# seek

DeepSeek-first Go coding agent harness. Architecture inspired by
[`earendil-works/pi`](https://github.com/earendil-works/pi).

Status: **M4.5 done** — TUI is daily-driver quality. Inline mode (no
alt-screen) so the terminal owns scrollback / copy / history; Esc
interrupts the agent mid-stream; per-call inline y/N approval for
bash and out-of-CWD writes; slash-command completion menu; `@`
file-path completion; ↑/↓ prompt history; tool spinners with
elapsed-time tails; token budget warning at 80% / 95% of model
context. Next: M5 (sessions + Skill loader + MCP). See
[`PRD.md`](./PRD.md).

## Quick start

```bash
export DEEPSEEK_API_KEY=sk-...

# Interactive TUI (when stdin is a TTY and no -p flag):
go run ./cmd/seek

# Print mode (when -p is set OR stdin is piped):
go run ./cmd/seek -p "Read README.md and summarise it in one sentence."
echo "What is 2+2?" | go run ./cmd/seek

# Allow bash and writes outside the working directory:
go run ./cmd/seek --yolo

# Use the reasoner (CoT prints dim):
go run ./cmd/seek -model deepseek-reasoner -p "Prove sqrt(2) is irrational."
```

In the TUI:

- **Enter** submits · **Ctrl+J** newline · **Ctrl+C** quits
- **Esc** — interrupt the agent mid-stream (or dismiss a menu, or deny an approval)
- **Ctrl+L** / `/clear` — clear the visible screen (your terminal keeps scrollback)
- **Ctrl+R** — toggle reasoning visibility on assistant messages
- **PgUp / PgDn / Ctrl+U / Ctrl+D** — scroll the terminal (or just use the mouse wheel)
- **↑ / ↓** — when input is empty, recall previous prompts; otherwise move the cursor
- **`/`** — opens the slash-command menu (Tab completes, ↑/↓ pick, Esc dismiss)
- **`@`** — opens the file-path picker over the current workspace (same Tab/↑/↓/Esc)

Slash commands (full list with `/help`):

| Command | What it does |
|---|---|
| `/help` | Show all commands and key bindings |
| `/clear` | Wipe the visible screen (agent state kept; scrollback preserved by the terminal) |
| `/reset` | Start a fresh conversation (agent state rebuilt) |
| `/model <id>` | Switch model mid-session (e.g. `/model deepseek-reasoner`) |
| `/yolo` | Toggle `--yolo` for the rest of the session |
| `/exit` | Quit |

When seek is started without `--yolo` it runs in **ask mode**: any `bash`
or write outside the working directory pops up an inline prompt:

```
⚠ approve bash "rm -rf node_modules"?
  [y] allow once  [n] deny  [a] always (yolo for session)  [Esc] deny
```

The status bar shows: model · streaming/idle · turn/tool counters · cache hit % · session cost · `ctx N%` (model context utilisation; tints yellow above 80%, red above 95%) · pricing tier with off-peak countdown. Assistant messages are rendered with Markdown via Glamour after they finish streaming.

**Inline mode**: seek does not enter alt-screen and does not capture
mouse events, so your terminal's native scrollback, mouse wheel,
click-and-drag selection, and Cmd+C copy all work across the entire
conversation. When you quit (`/exit` or Ctrl+C) the session stays
visible in the terminal.

When the response finishes, seek prints a stats footer on stderr:

```
--- seek stats ---
yolo:         false
model:        deepseek-chat
tier:         standard
turns:        5
tool calls:   4
ttfb:         1.273s
elapsed:      8.2s
prompt tok:   10987 (cache hit 7680 / miss 3307, ratio 69.9%)
completion:   864 tok
est. cost:    $0.0020 (saved ~7680 input tok via cache)
```

The `cache hit` / `miss` / `ratio` line is the DeepSeek prefix-cache
accounting — seek's main optimisation target (PRD §4.8.1). Stable
system prompt + tool schema + history is what lets the ratio climb across
turns.

## Tools

| Tool | What it does | Gated by |
|---|---|---|
| `read` | read a file with line numbers | — |
| `write` | create / overwrite a file | writes outside CWD need `--yolo` |
| `edit` | exact `old_string`→`new_string` substitution (Claude Code style) | edits outside CWD need `--yolo` |
| `bash` | run a shell command with timeout | needs `--yolo` |
| `fim_complete` | DeepSeek FIM endpoint — cheap gap-fill, returns text without applying | — |
| `think` | deepseek-reasoner bridge: multi-step planning or `reflect=true` self-review | — |

## Layout

```
cmd/seek/                  CLI entry
pkg/deepseek/              first-class DeepSeek client (chat, reasoner, FIM)
pkg/llm/                   thin generic interface (Anthropic / OpenAI / Gemini)
pkg/agent/                 agent runtime (event stream, tool loop, provider routing)
internal/{tui,session,tools,skill,mcp,cache,pricing,rpc}/  application internals
```

`pkg/deepseek` deliberately does **not** implement `pkg/llm.Provider`. See
PRD §4.1 for the rationale (DeepSeek-specific fields don't survive a
lowest-common-denominator interface).

## Develop

```bash
go test ./...
go vet ./...
go build ./...
```

## License

MIT (planned). Inspired by `earendil-works/pi` (MIT). NOTICE will be added
with the first tagged release.
