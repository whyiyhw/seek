# seek

**seek** 是一个基于 [DeepSeek](https://deepseek.com) 的编程助手。它在终端里运行，能读写文件、执行命令，帮你写代码——不用离开键盘。

**开源 (MIT) · 无地区限制 · 不收 telemetry · 欢迎全球用户**。你只需要一个 LLM provider 的 API key（DeepSeek 主力，也支持 Anthropic / OpenAI / Gemini）。

## 为什么选 seek

seek 跟 Claude Code / Aider / Cursor 在功能面上大量重叠——MCP、会话管理、IDE 集成、自定义 skill、权限系统都有。下面只列**真正差异化**的几项，其它能力作为"持平"列在最后，不强行打钩。

### 1. 便宜一个数量级

DeepSeek V4-Flash 的输入价格（来自 `internal/pricing/pricing.go`）：

| | seek (DeepSeek V4-Flash) | Claude Sonnet 4 |
|---|---|---|
| 输入（无缓存） | **$0.14 / 1M token** | $3 / 1M token |
| 输入（前缀缓存命中） | **$0.0028 / 1M token** | $0.30 / 1M token |
| 输出 | **$0.28 / 1M token** | $15 / 1M token |
| 错峰时段（北京时间 00:30–08:30） | **再 5 折** | — |

实测自举 benchmark cache hit 95.7%（前 5 轮除外 97%）——前缀缓存稳定的工程纪律换来真实的成本节约。状态栏实时显示命中率和节省的 token 数。

### 2. 单二进制，零依赖

`~5 MB`，无 Python / Node runtime，无 `npm install` / `pip install`。`go install github.com/whyiyhw/seek/cmd/seek@latest` 或者 release 页下 tarball，macOS / Linux / Windows 三平台。

### 3. DeepSeek 专属能力

- **V4 推理模式**（`Thinking.Type=enabled`）：通过 `think` 工具按需使用；内置 `dual-model` skill 做 reasoner→执行→reasoner 反思流程
- **FIM 端点**（`fim_complete` 工具）：小范围修改走填空补全，比 chat 便宜 5–10×
- **缓存命中率实时可见**：状态栏显示 hit ratio + 节省 token 数，让"如何写出缓存友好的 prompt"变成可观测的优化目标
- **错峰倒计时**：状态栏显示当前是否在 5 折时段，以及距下次切换还有多久

### 4. 中英文都流畅

工具描述、系统 prompt、错误信息提供中英双语；中文 prompt 在 DeepSeek 上响应优于多数欧美模型，是 seek 的核心使用场景之一。英文工作流没有任何限制——其它 provider（Anthropic / OpenAI / Gemini）默认英文路径，自然衔接。

### 通用能力（持平，非差异化）

下面这些 Claude Code / Cursor / Codex CLI 也有，列出来只是说明 seek 不缺：MCP 服务端接入、自定义 skill (`.md` + frontmatter)、会话持久化 / 分叉 (`/branch`) / 压缩 (`/compact`)、文件系统权限系统（默认询问 / `--yolo` 跳过 / 路径白名单）、JSON-RPC 2.0 服务模式（IDE 接入）、多 LLM provider（Anthropic / OpenAI / Gemini / OpenAI 兼容端点）。

[English version](./docs/README_EN.md)

## 快速上手

### 安装

**方式 1：预编译二进制（推荐，无需 Go 环境）**

macOS / Linux 一键下载最新 release：

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
VER=$(curl -fsSL https://api.github.com/repos/whyiyhw/seek/releases/latest | sed -nE 's/.*"tag_name":[[:space:]]*"v([^"]+)".*/\1/p')
curl -fsSL "https://github.com/whyiyhw/seek/releases/download/v${VER}/seek_${VER}_${OS}_${ARCH}.tar.gz" \
  | sudo tar -xz -C /usr/local/bin seek
```

不想用 `sudo`？换成你 PATH 里能写入的路径，比如 `tar -xz -C ~/.local/bin seek`。后续 `seek -upgrade` 会就地原子替换，再也不用走 curl 那一行。

Windows：从 [Releases 页面](https://github.com/whyiyhw/seek/releases/latest) 下载 `seek_*_windows_amd64.zip` 解压即用。

> **macOS 浏览器下载提示**：用 Safari/Chrome 下载会被 Gatekeeper 加 quarantine 属性，第一次运行会报错。`curl | tar` 管道提取不会触发；如果已经被打上，执行 `xattr -d com.apple.quarantine seek` 即可。

**方式 2：源码安装（需要 Go 1.25+）**

```bash
go install github.com/whyiyhw/seek/cmd/seek@latest
```

### 运行

```bash
# 启动 TUI（终端交互模式）
seek

# 或者非交互模式
seek -p "用一句话总结这个项目。"
```

**首次启动会引导你选 provider 并保存 API key 到 `~/.seek/config.json`**（权限 0600）——不需要手动 `export`。已有 env 变量（`DEEPSEEK_API_KEY` / `ANTHROPIC_API_KEY` / …）会优先，方便 CI 和一次性覆盖。

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

之后想换 key / 切 provider：TUI 内输入 `/setup` 重跑向导，或直接编辑 `~/.seek/config.json`。

详细用法：[`docs/`](./docs/) 包含会话、MCP、Skill 指南。  
TUI 内输入 `?` 查看所有快捷键和斜杠命令。

## 升级

```bash
seek -upgrade-check   # 是否有新版？只读，不改动二进制
seek -upgrade         # 拉取最新 release，校验 sha256，原地替换
seek -upgrade-dry-run # 走完下载+校验流程，跳过最后一步替换
```

`seek -upgrade` 从 [GitHub Releases](https://github.com/whyiyhw/seek/releases) 直接下载对应平台的二进制，sha256 对照 `checksums.txt` 验签后原子替换当前文件。本地 `go build` 出的开发版本默认会被拒绝覆盖（用 `-upgrade-force` 强制）。TUI 内也可输入 `/upgrade`。  
关闭启动时的版本检查：`export SEEK_NO_UPGRADE_CHECK=1`。

## 路线图

项目采用里程碑 M0–M7（已全部交付）。当前重点：

- **IDE 集成**：完善 `--rpc` 协议，开发编辑器插件
- **插件系统**：支持第三方工具加载
- **稳定化**：打 tag 发版，CI 加固

完整设计：[`PRD.md`](./docs/PRD.md)  
贡献者指南：[`AGENTS.md`](./AGENTS.md) 说明了架构约定。

## 开源 & 贡献

MIT 协议（计划中）。仓库公开：欢迎世界上所有地区的开发者使用、提 issue、提 PR——无地区限制，无身份审核，无强制 telemetry。

灵感来自 [`earendil-works/pi`](https://github.com/earendil-works/pi)（MIT）。架构约定见 [`AGENTS.md`](./AGENTS.md)，踩坑记录见 [`docs/pitfalls.md`](./docs/pitfalls.md)。

---

*seek — ~36k 行 Go 代码，38 个包，macOS / Linux / Windows 全平台 -race 测试通过。*
