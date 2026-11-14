# 更新日志（CHANGELOG）

## v0.11.0（2026-11-13）

v0.11.0 — seek grows a remote control plane: `seek serve` turns the binary into one long-running, channel-agnostic service you can drive from a chat app or a browser; GLM (Zhipu bigmodel.cn) joins the provider list; sub-agents can run on a model of their own; and `/distill` turns a finished session into a reviewable skill draft.

### Notable changes

- **`seek serve` — remote control plane (new)**. Run seek as one resident process reachable from any chat channel — Feishu ships today, anything else via the pluggable `Frontend` port. Each conversation binds to a project directory (`/bind` / `/projects` / `/switch` / `/new` / `/cancel`); messages exec the real `seek -p` inside that directory, keep the session id for multi-turn resume, serialize per conversation, and self-heal on crash. Graceful drain on shutdown; a macOS launchd template ships in `deploy/`. Guide: [`docs/guide-serve.md`](docs/guide-serve.md).
- **Web frontend**. `seek serve --frontend=web` serves an embedded chat page (zero build chain) with SSE streaming replies and token auth — point a browser at your machine or tailnet and drive seek from there.
- **GLM provider**. `glm` joins `deepseek` / `openai` / `anthropic` / `gemini`: set `GLM_API_KEY` (or a `providers.glm` block) and seek talks to the pinned bigmodel.cn OpenAI-compatible endpoint. glm-5.3 carries its own pricing card, so the live dollar stats stay accurate off the DeepSeek lane.
- **Per-sub-agent model routing**. The `agent` tool takes optional `provider` / `model` params — fan out cheap explore agents on one lane while the parent writes on another. Omitted params inherit the parent lane unchanged.
- **`/distill` — session → skill draft**. Distill the current session transcript into a SKILL.md draft under `~/.seek/skills/.drafts/` — drafts land in a dot directory no session ever loads, and installing one stays an explicit human `seek skill install`.
- **ACP headless hardening**. The `cwd` your editor passes with `session/new` now anchors every tool to that workspace (git, grep, read/edit/write/bash alike). For unattended runs, `SEEK_ACP_YOLO=1` auto-approves local actions while still blocking remote-mutating bash commands, and skips the cron auto-tick.
- **Fixed**: write/edit permission checks misjudged relative paths as "outside the workspace" whenever the shell sat behind a symlink (macOS `/tmp` → `/private/tmp` being the common case), wrongly denying edits. Both sides of the check now resolve symlinks through the same pipeline.
- **Quality**: behavioral eval suite grew 14 → 20 cases (session-id contract, print-mode permission semantics, tool-selection and edit-fallback regressions); three dead-code / gofmt / modernize sweeps.

## 已进 main、未部署
- c_r5dm2f：v0.11 发布准备：版本号 + CHANGELOG 汇总 + tag 草稿（发版动作留人）
- c_mvv5td：book 与 PRD 重复事实清单（缩并备料，只盘点不改）
- c_7afk5i：文档预算首轮缩并：budget.json 超限区开刀（每版一刀文档刀）
- c_jvn3ky：skill-distill 人审 CLI：/skill drafts 列表·预览·审批安装一条流
