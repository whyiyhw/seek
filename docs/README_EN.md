```
 ████ █████ █████ █   █
█     █     █     █  █
█     █     █     █ █
 ███  ████  ████  ██
    █ █     █     █ █
    █ █     █     █  █
████  █████ █████ █   █
```

**Languages**: [中文](../README.md) · English

**seek** is a coding agent powered by [DeepSeek](https://deepseek.com). It runs in your terminal, reads/writes files, executes commands, and helps you get work done — without leaving your keyboard.

**Open source (MIT) · no region lock · no telemetry · welcome from anywhere in the world**. All you need is an API key from one LLM provider — DeepSeek is the primary target, Anthropic / OpenAI / Gemini also supported.

## Why seek?

seek overlaps a lot with Claude Code / Aider / Cursor on features — MCP, session management, IDE integration, custom skills, permission systems all exist there too. This section only lists the things that are actually **differentiated**; the rest is mentioned at the end as "on par, not a differentiator" instead of forcing checkmarks against competitors.

### 1. An order of magnitude cheaper

DeepSeek input pricing (from `internal/pricing/pricing.go`):

| | seek (DeepSeek V4-Flash) | seek (DeepSeek V4-Pro) | Claude Sonnet 4 |
|---|---|---|---|
| Input (cache miss) | **$0.14 / 1M tokens** | **$0.435 / 1M tokens** (promo)¹ | $3 / 1M tokens |
| Input (prefix-cache hit) | **$0.0028 / 1M tokens** | **$0.003625 / 1M tokens** | $0.30 / 1M tokens |
| Output | **$0.28 / 1M tokens** | **$0.87 / 1M tokens** | $15 / 1M tokens |
| Off-peak window (00:30–08:30 Beijing time) | **50% off all of the above** | **50% off all of the above** | — |

> ¹ V4-Pro is currently at a 75%-off promotional rate; the full rack rate is $1.74 / $0.0145 / $3.48 (see `internal/pricing/pricing.go`). The `deepseek-chat` / `deepseek-reasoner` aliases route to V4-Flash pricing, not V4-Pro.

Self-hosting benchmark measures 95.7% cache hit (97% after turn 5) — the engineering discipline that keeps the prefix-cache stable (byte-stable schemas, system prompt, history) pays out in real cost savings. The status bar shows the hit ratio and saved-token count in real time.

### 2. Single binary, zero runtime deps

`~5 MB`, no Python / Node runtime, no `npm install` / `pip install`.
`go install github.com/whyiyhw/seek/cmd/seek@latest` — or grab a tarball from the Releases page — for macOS / Linux / Windows.

### 3. DeepSeek-specific affordances

- **V4 reasoning mode** (`Thinking.Type=enabled`): exposed as the `think` tool; the built-in `dual-model` skill chains reasoner → execute → reasoner-review for non-trivial multi-step tasks
- **FIM endpoint** (`fim_complete` tool): small-range edits go through DeepSeek's fill-in-the-middle endpoint, 5–10× cheaper than equivalent chat completions
- **Cache-hit visibility**: status bar shows hit ratio and saved tokens live, so "write cache-friendly prompts" becomes an observable optimization target instead of a vague best-practice
- **Off-peak countdown**: status bar shows the current pricing tier and how long until the next switch

### 4. Bilingual (Chinese + English)

Tool descriptions, system prompts, and error messages are provided in both English and Chinese; Chinese prompting on DeepSeek tends to outperform Western models on the same input, which is one of seek's core use cases. The English workflow has no limitations — and the other providers (Anthropic / OpenAI / Gemini) default to English paths regardless.

### On par (not a differentiator)

These are listed only to confirm seek isn't missing them — Claude Code / Cursor / Codex CLI have all of these too: MCP server integration, custom skills (`.md` + frontmatter), session persistence / fork (`/branch`) / compact (`/compact`), filesystem permission system (ask-by-default, `--yolo` to bypass, path scoping), JSON-RPC 2.0 server mode for IDE integration, multi-provider support (Anthropic / OpenAI / Gemini / OpenAI-compatible endpoints).

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

> Pressing Enter mid-stream **queues** a follow-up message (auto-sent when the current turn finishes). To **withdraw** a queued message, leave the textarea empty and press Enter again — softer than Esc, and the in-flight stream keeps running.

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

Detailed design: [`docs/prd/`](./prd/) (v0 initial · v1 current development).  
Contributor guide: See [`AGENTS.md`](../AGENTS.md) for architecture conventions.

## Open source & contributing

Licensed under the [MIT License](../LICENSE). The repo is public — developers from anywhere in the world are welcome to use it, file issues, and send pull requests. No region lock, no signup, no mandatory telemetry.

Inspired by [`earendil-works/pi`](https://github.com/earendil-works/pi) (MIT); attribution in [`NOTICE`](../NOTICE). Architecture conventions in [`AGENTS.md`](../AGENTS.md); ongoing pitfall log in [`docs/pitfalls.md`](./pitfalls.md).

---

*seek — ~36k lines of Go, 38 packages, tested with -race on macOS / Linux / Windows.*
