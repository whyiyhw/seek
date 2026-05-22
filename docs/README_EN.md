# seek

**seek** is a coding agent powered by [DeepSeek](https://deepseek.com). It runs in your terminal, reads/writes files, executes commands, and helps you get work done — without leaving your keyboard.

## Why seek?

**Single binary, ~5 MB** — zero runtime dependencies, download and run.

| Feature | seek | Claude Code | Aider |
|---------|------|-------------|-------|
| Binary size | ~5 MB ✅ 95%+ smaller | ~100 MB (npm) | ~50 MB (pip) |
| Input cost / 1M tokens | $0.14 (miss) / $0.0028 (hit) — 99% cheaper | $3.00 (Sonnet) | Bring your own API key |
| TUI experience | ✅ inline mode + queue & interject | ⚠️ alt-screen fullscreen | ❌ CLI-only |
| Session management (branch / compact / resume) | ✅ full commands | ❌ limited | ❌ |
| Cache hit ratio visualization | ✅ real-time in status bar | ❌ | ❌ |
| FIM (fill-in-the-middle) | ✅ standalone cheap endpoint | ❌ | ❌ |
| Off-peak pricing countdown | ✅ visible countdown | ❌ | ❌ |
| Dual-model reasoning (think + chat) | ✅ think tool + dual-model skill | ❌ | ❌ |
| Permission system | ✅ fine-grained control | ❌ | ❌ |
| MCP support | ✅ native | ✅ supported | ❌ |
| Custom skills | ✅ defined via .md files | ✅ Slash commands | ❌ |
| IDE integration | ✅ JSON-RPC 2.0 server | ❌ | ⚠️ plugins, non-standard |

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
# TUI mode (when stdin is a terminal)
seek

# Or non-interactively
seek -p "Explain this project in one sentence."
```

**First launch walks you through picking a provider and saves the API key to `~/.seek/config.json`** (perms 0600) — no manual `export` needed. Existing env vars (`DEEPSEEK_API_KEY` / `ANTHROPIC_API_KEY` / …) take priority, which is convenient for CI and one-off overrides.

```
$ seek
  seek — first-run setup
  ──────────────────────
  Step 1/2 — choose a provider:
    1) DeepSeek (recommended)
    2) Anthropic Claude
    3) OpenAI GPT
    4) Google Gemini
  > 1
  Step 2/2 — paste your DeepSeek API key:
    Get one from https://platform.deepseek.com/api_keys
  > sk-...
  Verifying with a 1-token ping... ok.
  Saved to ~/.seek/config.json.
```

Need to switch keys / providers later? Type `/setup` inside the TUI to re-run the wizard, or hand-edit `~/.seek/config.json`.

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
