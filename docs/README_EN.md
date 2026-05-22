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

### Install

**Option 1: prebuilt binary (recommended — no Go toolchain needed)**

macOS / Linux one-liner that pulls the latest release:

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
VER=$(curl -fsSL https://api.github.com/repos/whyiyhw/seek/releases/latest | sed -nE 's/.*"tag_name":[[:space:]]*"v([^"]+)".*/\1/p')
curl -fsSL "https://github.com/whyiyhw/seek/releases/download/v${VER}/seek_${VER}_${OS}_${ARCH}.tar.gz" \
  | sudo tar -xz -C /usr/local/bin seek
```

Don't want `sudo`? Pick a writable directory on your `PATH`, e.g. `tar -xz -C ~/.local/bin seek`. Subsequent updates use `seek -upgrade` for an atomic in-place replace — no need to re-run the curl line.

Windows: grab `seek_*_windows_amd64.zip` from the [Releases page](https://github.com/whyiyhw/seek/releases/latest) and unpack it.

> **macOS Gatekeeper note**: Safari/Chrome downloads get the `com.apple.quarantine` xattr and Gatekeeper will block first-run. The `curl | tar` pipeline above does **not** trigger this; if you already hit it, run `xattr -d com.apple.quarantine seek` to clear it.

**Option 2: install from source (requires Go 1.25+)**

```bash
go install github.com/whyiyhw/seek/cmd/seek@latest
```

### Run

```bash
# Set your API key
export DEEPSEEK_API_KEY=sk-...

# Run (TUI when stdin is a terminal)
seek

# Or use it non-interactively
seek -p "Explain this project in one sentence."
```

See [`docs/`](./) for sessions, MCP, and skills guides.  
See `?` inside the TUI for all key bindings and slash commands.

## Upgrade

```bash
seek -upgrade-check   # is a newer release out? read-only, never touches the binary
seek -upgrade         # pull the latest release, verify sha256, replace in place
seek -upgrade-dry-run # run download + checksum verification, skip the final replace
```

`seek -upgrade` downloads the platform binary directly from [GitHub Releases](https://github.com/whyiyhw/seek/releases), verifies its sha256 against the release's `checksums.txt`, and atomically replaces the running binary. Local `go build`-style dev binaries are refused by default (use `-upgrade-force` to override). From inside the TUI you can also type `/upgrade`.  
Disable the startup version-check probe: `export SEEK_NO_UPGRADE_CHECK=1`.

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
