# seek

DeepSeek-first Go coding agent harness. Architecture inspired by
[`earendil-works/pi`](https://github.com/earendil-works/pi).

Status: **M0** — `pkg/deepseek` streaming client + minimal CLI. Tool calling,
TUI, MCP, skills, and second-class providers (Anthropic / OpenAI / Gemini)
land in subsequent milestones. See [`PRD.md`](./PRD.md).

## Quick start

```bash
export DEEPSEEK_API_KEY=sk-...
go run ./cmd/seek -p "Say hi in one sentence."
echo "What is 2+2?" | go run ./cmd/seek
go run ./cmd/seek -model deepseek-reasoner -p "Prove sqrt(2) is irrational."
```

When the response finishes, seek prints a stats footer on stderr:

```
--- seek stats ---
finish:      stop
ttfb:        612ms
elapsed:     2.314s
prompt tok:  42 (cache hit 0 / miss 42, ratio 0.0%)
completion:  31 tok
```

The `cache hit` / `miss` / `ratio` line is the DeepSeek prefix-cache
accounting — seek's main optimisation target (see PRD §4.8.1).

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
