# seek

DeepSeek-first Go coding agent harness. Architecture inspired by
[`earendil-works/pi`](https://github.com/earendil-works/pi).

Status: **M2** — DeepSeek streaming client, agent loop, five tools
(`read` / `write` / `edit` / `bash` / `fim_complete`), permission gating.
TUI, MCP, skills, and second-class providers (Anthropic / OpenAI / Gemini)
land in subsequent milestones. See [`PRD.md`](./PRD.md).

## Quick start

```bash
export DEEPSEEK_API_KEY=sk-...
go run ./cmd/seek -p "Read README.md and summarise it in one sentence."

# Allow bash and writes outside the working directory:
go run ./cmd/seek --yolo -p "Write a Go hello world to /tmp/h.go and run it."

# Use the reasoner (CoT prints dim to stderr):
go run ./cmd/seek -model deepseek-reasoner -p "Prove sqrt(2) is irrational."
```

When the response finishes, seek prints a stats footer on stderr:

```
--- seek stats ---
yolo:         true
turns:        4
tool calls:   3
ttfb:         1.273s
elapsed:      8.183s
prompt tok:   6868 (cache hit 4864 / miss 2004, ratio 70.8%)
completion:   381 tok
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
