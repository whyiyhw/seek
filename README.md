<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white" alt="Go Version">
  <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT License">
  <img src="https://img.shields.io/badge/CI-passing-brightgreen?logo=github" alt="CI">
  <img src="https://img.shields.io/badge/PRs-welcome-brightgreen" alt="PRs Welcome">
</p>

**Languages**: 中文 · [English](./docs/README_EN.md)

# seek

**终端里的编程助手**——基于 DeepSeek / Anthropic / OpenAI，读写文件、执行命令、帮你写代码。不用离开键盘。

> 开源 (MIT) · 无地区限制 · 无 telemetry · 欢迎全球用户

---

## ⚡ 快速开始

**macOS / Linux**（无需 Go 环境，~5 MB 单二进制）：

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
VER=$(curl -fsSL https://api.github.com/repos/whyiyhw/seek/releases/latest | sed -nE 's/.*"tag_name":[[:space:]]*"v([^"]+)".*/\1/p')
curl -fsSL "https://github.com/whyiyhw/seek/releases/download/v${VER}/seek_${VER}_${OS}_${ARCH}.tar.gz" \
  | sudo tar -xz -C /usr/local/bin seek
seek
```

首次运行引导设置 API key，之后即可开始对话。详细步骤：[安装指南](./docs/)

**Windows**：从 [Releases](https://github.com/whyiyhw/seek/releases/latest) 下载 `seek_*_windows_amd64.zip` 解压到固定目录并[加入 PATH](./docs/guide-windows.md) 后即可在终端输入 `seek`。TUI 请在 **[Windows Terminal](https://github.com/microsoft/terminal)** 中运行（[安装说明](./docs/guide-windows.md)）；不要用蓝色老式 PowerShell 窗口。

> macOS Gatekeeper 问题：`curl | tar` 管道不会触发；若浏览器下载被打上 quarantine，执行 `xattr -d com.apple.quarantine seek` 即可。

**升级**：`seek -upgrade` 自动拉取最新 release，校验 sha256，原子替换。TUI 内也可 `/upgrade`。

---

## 🎯 为什么选 seek

### 💰 便宜一个数量级

DeepSeek 输入价格（来源：`internal/pricing/pricing.go`）：

| 对比项 | DeepSeek V4-Flash | DeepSeek V4-Pro | Claude Sonnet 4 |
|---|---|---|---|
| 输入（无缓存） | **$0.14** / 1M tok | **$0.435** / 1M tok¹ | $3 / 1M tok |
| 输入（前缀缓存命中） | **$0.0028** / 1M tok | **$0.003625** / 1M tok | $0.30 / 1M tok |
| 输出 | **$0.28** / 1M tok | **$0.87** / 1M tok | $15 / 1M tok |
| 错峰折扣² | **再 5 折** | **再 5 折** | — |

> ¹ V4-Pro 目前 promo 价（75% 折扣）；全价 $1.74 / $0.0145 / $3.48。  
> ² 北京时间 00:30–08:30。

实测 prefix-cache hit 率 **95.7%**（前 5 轮除外 97%）——工程纪律写入工具，成本节约在状态栏实时可见。

### 📦 单二进制零依赖

`~5 MB`，无 Python / Node runtime，无 `npm install` / `pip install`。`go install github.com/whyiyhw/seek/cmd/seek@latest` 或 release tarball，macOS / Linux / Windows 三平台。

### 🧠 三层记忆（L/M/S）

| 层级 | 名称 | 作用 |
|---|---|---|
| **S** (短期) | 会话记忆 | 自动保存完整消息历史，支持 `/branch` 分叉和 `/compact` 压缩 |
| **M** (中期) | 项目记忆 | `memory_observe` 写入关键决策，`memory_recall` 检索，decay-score GC 自动遗忘 |
| **L** (长期) | 用户本源 | `seek -dream` 跨项目归纳用户偏好，常驻 system prompt |

Claude Code / Cursor 仅有会话持久化——缺少跨会话的项目记忆和用户偏好归纳。

### 🎯 DeepSeek 专属能力

- **V4 推理模式**（`Thinking.Type=enabled`）：通过 `think` 工具按需调用；内置 `dual-model` skill 做 reasoner → 执行 → 反思
- **FIM 端点**（`fim_complete`）：小范围修改走填空补全，比 chat 便宜 5–10×
- **缓存命中率实时可见**：状态栏显示 hit ratio + 节省 token 数
- **错峰倒计时**：状态栏显示当前是否在 5 折时段，以及距下次切换时间

### 🖥️ TUI 原生交互流

- **`/plan`** — 只读探索模式，审计 agent 计划而不动文件
- **`/steer`** — 流中插入指令（macOS 友好的 Alt+Enter 替代方案）
- **`/review`** — 一键代码审查：激活 plan + 审查 prompt
- **`ask_user`** — 模型可以打开 TUI 选择器征求你的决定
- **空 Enter 撤回** — 有排队消息时，空输入框按 Enter 撤回

### 🌏 中英双语

工具描述、system prompt、错误信息中英双语。中文 prompt 在 DeepSeek 上响应优于多数欧美模型，是 seek 的核心使用场景之一。英文工作流无限制——其他 provider 默认英文路径，自然衔接。

---

## 📚 Skills & 生态

兼容 [Anthropic Agent Skills 格式](https://docs.anthropic.com/en/docs/claude-code/skills)（`<dir>/SKILL.md` + frontmatter），任何 Claude Code skill 仓库可零修改安装。

```bash
seek skill create <name>              # 创建 skill
seek skill install ./my-skill         # 本地路径安装
seek skill install https://github.com/foo/bar#v1.0.0  # Git URL
seek skill list                       # 查看已加载
seek skill stats --top 5              # 调用排行
```

所有命令 TUI 内可用：`/skill <verb>`。单文件 `.md` skill 永久兼容。

**其他生态能力**：MCP 服务端接入 · 文件系统权限系统（默认询问 / `--yolo` / 路径白名单）· JSON-RPC 2.0 服务模式（IDE 接入）· 多 LLM provider（Anthropic / OpenAI / Gemini / OpenAI 兼容端点）。

---

## 📖 路线图

里程碑 **M0–M9 全部交付**。最近新增（v0.4.x+）：

| 功能 | 说明 |
|---|---|
| `ask_user` 工具 | 模型可打开 TUI 选择器征求你的决定 |
| `skill_fetch` / `skill_commit` | 模型可直接获取并安装 skill（需审批） |
| `/plan` · `/steer` · `/review` | TUI 交互升级 |
| Skill v2 目录包 | Git URL / HTTPS 压缩包 / 本地路径安装 |
| **Checkpoint 撤销安全网** | git 每 turn 快照 + 文件级 undo/redo，`/undo` `/redo` `/restore` |
| **Shell Hooks** | 工具调用前后 shell 钩子，`.seek/hooks.toml` 可配置 |
| Windows 安装 | `seek -install` 自动加入 PATH，首次运行提示 |

完整设计文档：[`docs/prd/`](./docs/prd/) | 贡献指南：[`AGENTS.md`](./AGENTS.md)

---

## 🔓 开源 & 贡献

[MIT 协议](./LICENSE)。欢迎所有地区开发者使用、提 issue、提 PR——无地区限制，无身份审核，无强制 telemetry。

灵感来自 [`earendil-works/pi`](https://github.com/earendil-works/pi)（MIT）；归属说明见 [`NOTICE`](./NOTICE)。踩坑记录见 [`docs/pitfalls.md`](./docs/pitfalls.md)；Windows TUI 见 [`docs/guide-windows.md`](./docs/guide-windows.md)。

---

*seek — ~49k 行 Go（25k 非测试），44 个包，macOS / Linux / Windows 全平台 -race 测试通过。*
