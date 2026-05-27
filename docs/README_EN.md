<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white" alt="Go Version">
  <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT License">
  <img src="https://img.shields.io/badge/CI-passing-brightgreen?logo=github" alt="CI">
  <img src="https://img.shields.io/badge/PRs-welcome-brightgreen" alt="PRs Welcome">
</p>

**Languages**: [中文](../README.md) · English

# seek

**A coding agent that runs in your terminal** — powered by DeepSeek / Anthropic / OpenAI. Reads files, writes code, runs commands. You never leave the keyboard.

> Open source (MIT) · no region lock · no telemetry · welcome from anywhere in the world

---

## ⚡ Quick start

**macOS / Linux** (no Go toolchain needed — ~5 MB single binary):

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
VER=$(curl -fsSL https://api.github.com/repos/whyiyhw/seek/releases/latest | sed -nE 's/.*"tag_name":[[:space:]]*"v([^"]+)".*/\1/p')
curl -fsSL "https://github.com/whyiyhw/seek/releases/download/v${VER}/seek_${VER}_${OS}_${ARCH}.tar.gz" \
  | sudo tar -xz -C /usr/local/bin seek
seek
```

First launch walks you through picking a provider and saving the API key — that's it. Detailed walkthrough: [`docs/`](./).

**Windows**: grab `seek_*_windows_amd64.zip` from [Releases](https://github.com/whyiyhw/seek/releases/latest), extract to a permanent folder, and [add it to your PATH](./guide-windows.md). Run the TUI inside **[Windows Terminal](https://github.com/microsoft/terminal)** — see [`docs/guide-windows.md`](./guide-windows.md) for install steps. Avoid the legacy blue PowerShell 5.x window.

> **macOS Gatekeeper**: `curl | tar` doesn't trigger the quarantine xattr. If your browser download is blocked, run `xattr -d com.apple.quarantine seek`.

**Upgrade**: `seek -upgrade` pulls the latest release, verifies sha256, atomically replaces the binary. Or `/upgrade` inside the TUI.

---

## 🎯 Why seek?

### 💰 An order of magnitude cheaper

DeepSeek input pricing (from `internal/pricing/pricing.go`):

| Metric | DeepSeek V4-Flash | DeepSeek V4-Pro | Claude Sonnet 4 |
|---|---|---|---|
| Input (cache miss) | **$0.14** / 1M tok | **$0.435** / 1M tok¹ | $3 / 1M tok |
| Input (prefix-cache hit) | **$0.0028** / 1M tok | **$0.003625** / 1M tok | $0.30 / 1M tok |
| Output | **$0.28** / 1M tok | **$0.87** / 1M tok | $15 / 1M tok |
| Off-peak window² | **50% off all of the above** | **50% off all of the above** | — |

> ¹ V4-Pro is currently at a 75%-off promotional rate; the full rack rate is $1.74 / $0.0145 / $3.48.  
> ² 00:30–08:30 Beijing time.

Measured prefix-cache hit rate: **95.7%** (97% after turn 5) — real engineering discipline paying out in real cost savings. The status bar shows hit ratio and saved tokens live.

### 📦 Single binary, zero runtime deps

`~5 MB`, no Python / Node runtime, no `npm install` / `pip install`. `go install github.com/whyiyhw/seek/cmd/seek@latest` or grab a release tarball. macOS / Linux / Windows.

### 🧠 Three-tier memory (L/M/S)

| Tier | Name | What it does |
|---|---|---|
| **S** (short-term) | Session memory | Full message history auto-saved; `/branch` fork and `/compact` compression |
| **M** (mid-term) | Project memory | `memory_observe` writes key decisions; `memory_recall` retrieves; decay-score GC auto-forgets |
| **L** (long-term) | Soul memory | `seek -dream` cross-project preference distillation, resident in system prompt |

Claude Code / Cursor only have session persistence — no cross-session project memory or user-level preference induction.

### 🎯 DeepSeek-specific affordances

- **V4 reasoning mode** (`Thinking.Type=enabled`): exposed as the `think` tool; the built-in `dual-model` skill chains reasoner → execute → reasoner-review
- **FIM endpoint** (`fim_complete`): fill-in-the-middle for small-range edits, 5–10× cheaper than chat
- **Cache-hit visibility live**: status bar shows hit ratio and saved tokens in real time
- **Off-peak countdown**: status bar shows the current pricing tier and time to next switch

### 🖥️ TUI-native interaction flow

- **`/plan`** — read-only exploration: audit what the agent would do without touching files
- **`/steer`** — mid-stream instruction insertion (Mac-friendly Alt+Enter alternative)
- **`/review`** — one-shot code review: plan mode + review prompt in one command
- **`ask_user`** — the model opens an inline TUI picker when it needs a decision from you
- **Empty-Enter withdraw** — with a queued message, pressing Enter on an empty textarea withdraws it

### 🌏 Bilingual (Chinese + English)

Tool descriptions, system prompts, and error messages in both languages. Chinese prompting on DeepSeek tends to outperform Western models on the same input — a core use case. English workflow has no limitations; other providers (Anthropic / OpenAI / Gemini) default to English.

---

## 📚 Skills & ecosystem

Compatible with the [Anthropic Agent Skills format](https://docs.anthropic.com/en/docs/claude-code/skills) (`<dir>/SKILL.md` + frontmatter). Any Claude Code skill repo installs without modification.

```bash
seek skill create <name>              # Create a skill
seek skill install ./my-skill         # Local path
seek skill install https://github.com/foo/bar#v1.0.0  # Git URL
seek skill list                       # List loaded skills
seek skill stats --top 5              # Call statistics
```

All commands available inside the TUI: `/skill <verb>`. Single-file `.md` skills remain fully supported.

**Other ecosystem features**: MCP server integration · filesystem permission system (ask-by-default, `--yolo`, path scoping) · JSON-RPC 2.0 server mode (IDE integration) · multi-provider (Anthropic / OpenAI / Gemini / OpenAI-compatible endpoints).

---

## 📖 Roadmap

Milestones **M0–M8 all delivered**. Recently shipped (v0.3.x+):

| Feature | What it does |
|---|---|
| `ask_user` tool | Model opens a TUI picker when it needs a decision from you |
| `skill_fetch` / `skill_commit` | Model can fetch and install skills directly (with your approval) |
| `/plan` · `/steer` · `/review` | TUI interaction upgrades |
| Skill v2 package install | Git URL, HTTPS tarball, local path |

Full design docs: [`docs/prd/`](./prd/) | Contributor guide: [`AGENTS.md`](../AGENTS.md)

---

## 🔓 Open source & contributing

Licensed under the [MIT License](../LICENSE). Developers from anywhere in the world are welcome — no region lock, no signup, no mandatory telemetry.

Inspired by [`earendil-works/pi`](https://github.com/earendil-works/pi) (MIT); attribution in [`NOTICE`](../NOTICE). Architecture conventions in [`AGENTS.md`](../AGENTS.md); pitfall log in [`docs/pitfalls.md`](./pitfalls.md).

---

*seek — ~49k lines of Go (25k non-test), 44 packages, tested with -race on macOS / Linux / Windows.*
