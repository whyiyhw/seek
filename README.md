# seek

DeepSeek-first Go coding agent harness. Architecture inspired by
[`earendil-works/pi`](https://github.com/earendil-works/pi).

Status: **M4** — Adds the Bubble Tea TUI (interactive multi-turn
session, live status bar with cache ratio + cost + off-peak countdown,
streaming render, tool indicators). Print mode preserved for piping
and scripting. MCP, skills, and second-class providers (Anthropic /
OpenAI / Gemini) land in subsequent milestones. See [`PRD.md`](./PRD.md).

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

- Type your prompt, **Enter** to submit, **Ctrl+J** for a newline
- **Ctrl+L** clears the visible conversation (history-only — agent state preserved)
- **Ctrl+C** quits
- The status bar shows: model · streaming/idle · turn/tool counters · cache hit % · session cost · pricing tier with off-peak countdown

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
