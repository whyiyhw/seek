# seek

**seek** 是一个基于 [DeepSeek](https://deepseek.com) 的编程助手。它在终端里运行，能读写文件、执行命令，帮你写代码——不用离开键盘。

## 核心优势

**单二进制，仅 ~5 MB** — 零运行时依赖，下载即用。

| 指标 | seek | Claude Code | Aider |
|------|------|-------------|-------|
| 包大小 | ~5 MB ✅ 小 95%+ | ~100 MB (npm) | ~50 MB (pip) |
| 输入费用 / 1M tokens | $0.14 (miss) / $0.0028 (hit) — 便宜 99% | $3.00 (Sonnet) | 自备 API Key |
| TUI 交互 | ✅ 内联模式 + 排队插话 | ⚠️ alt-screen 全屏 | ❌ 纯 CLI |
| 会话管理（branch / compact / resume） | ✅ 完整命令 | ❌ 有限 | ❌ |
| 缓存命中率可视化 | ✅ 状态栏实时 | ❌ | ❌ |
| FIM 填空补全 | ✅ 独立低成本端点 | ❌ | ❌ |
| 错峰计价倒计时 | ✅ 可见倒计时 | ❌ | ❌ |
| 双模型推理（reasoner + chat） | ✅ think 工具 + 双模型 skill | ❌ | ❌ |
| 权限系统 | ✅ 细粒度控制 | ❌ | ❌ |
| MCP 扩展 | ✅ 原生支持 | ✅ 支持 | ❌ |
| 自定义 Skill | ✅ .md 文件定义 | ✅ Slash 命令 | ❌ |
| IDE 集成 | ✅ JSON-RPC 2.0 服务 | ❌ | ⚠️ 插件，非标准 |

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

## 协议

MIT（计划中）。灵感来自 [`earendil-works/pi`](https://github.com/earendil-works/pi)（MIT）。

---

*seek — ~36k 行 Go 代码，38 个包，macOS / Linux / Windows 全平台 -race 测试通过。*
