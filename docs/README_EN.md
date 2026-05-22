# seek

**seek** is a coding agent powered by [DeepSeek](https://deepseek.com). It runs in your terminal, reads/writes files, executes commands, and helps you get work done — without leaving your keyboard.

## Why seek?

| Compared to... | seek gives you |
|---|---|
| **Claude Code** | ~20× cheaper input than Claude Sonnet (V4-Flash $0.14/M vs $3/M); prefix-cache hits drop another 50× to $0.0028/M. Native V4 reasoning mode (`Thinking.Type=enabled`) + FIM endpoint for cheap gap-fills. |
| **Aider** | A real interactive TUI. Type mid-stream, queue messages, steer the agent mid-turn. Full session management with `/branch`, `/compact`, `/resume`. |
| **Generic LLM agents** | DeepSeek-optimized: cache hit ratio shown in real-time, off-peak pricing countdown, dual-model skill (V4 reasoning mode for planning + chat for execution). Also supports Anthropic / OpenAI / Gemini as fallback providers. |

### What else?

- **Inline mode** — no alt-screen. Your terminal history, scrollback, and mouse selection work normally. Quit and the conversation stays visible.
- **Permission system** — safe by default. `bash` and out-of-tree writes need approval; `--yolo` disables the guard for power users.
- **JSON-RPC 2.0 server** (`--rpc`) — plug seek into your IDE.
- **MCP support** — load external tools from any MCP server.
- **Custom skills** — write your own `.md` instructions that seek loads and follows.

## Quick start

```bash
# Install
go install github.com/whyiyhw/seek/cmd/seek@latest

# Set your API key
export DEEPSEEK_API_KEY=sk-...

# Run (TUI when stdin is a terminal)
seek

# Or use it non-interactively
seek -p "Explain this project in one sentence."
```

See [`docs/`](./docs/) for sessions, MCP, and skills guides.  
See `?` inside the TUI for all key bindings and slash commands.

## Roadmap

The project follows milestones M0–M7 (all delivered). Current focus:

- **IDE integration**: refine `--rpc` protocol, add editor plugins
- **Plugin system**: third-party tool loading
- **Stabilization**: tagged releases, CI hardening

Detailed design: [`PRD.md`](./PRD.md).  
Contributor guide: See [`AGENTS.md`](../AGENTS.md) for architecture conventions.

## License

MIT (planned). Inspired by [`earendil-works/pi`](https://github.com/earendil-works/pi) (MIT).

---

*seek — ~36k lines of Go, 38 packages, tested with -race on macOS / Linux / Windows.*
