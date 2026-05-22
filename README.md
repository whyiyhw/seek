# seek

**seek** 是一个基于 [DeepSeek](https://deepseek.com) 的编程助手。它在终端里运行，能读写文件、执行命令，帮你写代码——不用离开键盘。

## 牛在哪

| 对比 | seek 的优势 |
|---|---|
| **Claude Code** | 输入便宜约 20×（V4-Flash $0.14/M vs Claude Sonnet $3/M），前缀缓存命中后再降 50× 到 $0.0028/M。原生 V4 推理模式（`Thinking.Type=enabled`）+ FIM 端点，小修改走填空补全。 |
| **Aider** | 真正的交互式 TUI。一边输出一边继续打字、排队、插话。完整的会话管理：`/branch` 分叉、`/compact` 压缩、`/resume` 恢复。 |
| **通用 Agent 框架** | 专为 DeepSeek 优化：实时显示缓存命中率、错峰计价倒计时、双模型 skill（V4 推理模式做规划 + chat 做执行）。也支持 Anthropic / OpenAI / Gemini 作为备选。 |

### 还有什么

- **Inline 模式** — 不进 alt-screen。终端滚动、鼠标选取、Cmd+C 复制全程正常。退出后对话留在终端里。
- **安全机制** — 默认询问模式。`bash` 和写工作目录外的文件需要确认；`--yolo` 关闭保护给高级用户。
- **JSON-RPC 2.0 服务端**（`--rpc`）— 接入 IDE。
- **MCP 支持** — 加载任意 MCP 服务端的外部工具。
- **自定义 Skill** — 写一份 `.md` 文件，seek 就会加载并执行。

[English version](./docs/README_EN.md)

## 快速上手

```bash
# 安装
go install github.com/whyiyhw/seek/cmd/seek@latest

# 设置 API Key
export DEEPSEEK_API_KEY=sk-...

# 启动 TUI（终端交互模式）
seek

# 或者非交互模式
seek -p "用一句话总结这个项目。"
```

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
