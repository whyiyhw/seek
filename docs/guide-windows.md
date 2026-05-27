# Windows 终端与 TUI / Windows Terminal & TUI Guide

seek 在 Windows 上的 TUI **推荐使用 [Windows Terminal](https://github.com/microsoft/terminal)**。在 Windows Terminal 里运行 PowerShell 或 CMD，流式输出、中文显示、键盘输入均可正常工作。

> **English**: seek's TUI on Windows is best experienced inside **[Windows Terminal](https://github.com/microsoft/terminal)**. Run PowerShell or CMD in WT for correct streaming output, Chinese text rendering, and keyboard input.

## 安装 Windows Terminal / Install

任选一种方式 / Pick one:

### winget（推荐 / Recommended）

在 PowerShell 或 CMD 中执行 / Run in PowerShell or CMD:

```powershell
winget install --id Microsoft.WindowsTerminal -e
```

### Microsoft Store

1. 打开 **Microsoft Store**（微软商店）
2. 搜索 **Windows Terminal**
3. 点击 **获取 / 安装**

> **English**: Open Microsoft Store, search for "Windows Terminal", and click Install.

### GitHub 手动下载 / Manual download

1. 打开 [Windows Terminal Releases](https://github.com/microsoft/terminal/releases/latest)
2. 下载 `.msixbundle` 安装包
3. 双击安装

> **English**: Download the latest `.msixbundle` from GitHub Releases and double-click to install.

## 安装后怎么用 seek / Usage

1. 打开 **Windows Terminal**（开始菜单搜索「Terminal」）
2. 新建 **PowerShell** 或 **CMD** 标签页
3. 运行 seek：

```powershell
seek
```

首次运行会引导设置 API Key。

> **English**: Open Windows Terminal, create a PowerShell or CMD tab, and run `seek`. The first launch will walk you through API key setup.

## 设为默认终端（可选）/ Set as default (optional)

**设置 → 隐私和安全性 → 开发者选项 → 终端 → 将 Windows Terminal 设为默认终端应用**

或在 Windows Terminal **设置 → 启动** 中设为默认。

> **English**: Go to Settings → Privacy & security → For developers → Terminal → set "Windows Terminal" as the default. Alternatively, set it in Windows Terminal's own startup settings.

## 为什么不推荐蓝色 PowerShell 5.x 窗口 / Why not the legacy blue window

老式 **conhost**（蓝色背景的 Windows PowerShell 5.x）对 ANSI 光标控制支持较弱。seek TUI 的流式输出依赖原地刷新，在老式窗口中可能出现：

- 每行 token 单独占一行（「阶梯式刷屏」）
- 左侧出现乱码字符
- 中文显示异常

**换用 Windows Terminal 即可解决**，无需额外配置。

> **English**: The legacy conhost (blue-background Windows PowerShell 5.x) has poor ANSI cursor control. seek's TUI relies on in-place refresh, which can produce a "staircase" effect, garbled characters, or broken Chinese text. Switching to Windows Terminal fixes all of this — no extra configuration needed.

## 临时替代：print 模式 / Alternative: print mode

若暂时无法安装 Windows Terminal，可用不依赖 TUI 键盘的 print 模式：

> If you cannot install Windows Terminal right now, use seek's print mode (no TUI required):

```powershell
seek -p "你的问题 / your question"
```

## 安装 seek 本身 / Installing seek

从 [Releases](https://github.com/whyiyhw/seek/releases/latest) 下载 `seek_*_windows_amd64.zip` 解压，或：

> Download from [Releases](https://github.com/whyiyhw/seek/releases/latest) or run:

```powershell
go install github.com/whyiyhw/seek/cmd/seek@latest
```

升级 / Upgrade：`seek -upgrade`
