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

### 多行粘贴 / Multi-line paste

在 TUI 输入框粘贴多行文本时，请用 **Ctrl+V**（或 Windows Terminal 的 **Ctrl+Shift+V**）——seek 直接从剪贴板读取，绕过终端。粘贴完成后按 **Enter** 发送（超过 3 行会显示 `📋 pasted N lines` 占位符，再按 Enter 即可）。

> **English**: Paste multi-line prompts with **Ctrl+V** (or WT's **Ctrl+Shift+V**) — seek reads the clipboard directly, bypassing the terminal. Then press **Enter** to send. Long pastes fold into a `📋 pasted N lines` marker — press **Enter** again to submit.

> **Windows Terminal 中文输入法（IME）用户注意**：在 WT 中按 Enter 提交输入法候选词时，WT 会把提交的文字和 Enter 键一并转发给 seek，因此**提交候选词的 Enter 会直接发送消息**（这是设计行为，不会变成换行）。「按 Enter 变成换行」是旧版本的 bug（已修复）：现在的行为是仅当运行在旧版 Windows 控制台 conhost 中时，粘贴产生的回车才会被当作换行；如需强制切换，可设置 `SEEK_LEGACY_CONHOST_INPUT=1`。
>
> **English (IME users)**: When Windows Terminal forwards the Enter that commits an IME composition, that Enter sends the message directly — it is NOT turned into a newline. The "Enter inserts a newline" bug (Windows Terminal + Chinese IME) is fixed: the 50ms paste-guard now only applies inside the legacy console host (conhost), where pasted CRLF lines still need it. Set `SEEK_LEGACY_CONHOST_INPUT=1` to force the legacy behavior.

### 输入框换行 / Newline in the input box

| 按键 | 行为 |
|------|------|
| **Enter** | 发送消息 |
| **Ctrl+J** | 插入换行（跨平台官方方式） |
| **Shift+Enter** | 插入换行（在支持修饰键的终端：Windows Terminal / conhost、kitty、wezterm、foot、ghostty 等） |

**Shift+Enter 换行是渐进增强**：seek 基于 Bubble Tea v2，能收到带修饰键的按键事件。在支持 CSI-u / kitty 键盘协议（Unix）或保留控制台修饰键（Windows）的终端上，Shift+Enter 在输入框内插入换行；在**不支持**修饰键的少数终端上，Shift+Enter 会退化为普通 Enter（直接发送消息）——与旧版本行为一致，不会误伤。**Ctrl+J 在任何终端都可用**，是跨平台保底方式。

> **English**: **Enter** sends; **Ctrl+J** inserts a newline everywhere; **Shift+Enter** also inserts a newline on terminals that report modifiers (Windows Terminal, conhost, kitty, wezterm, foot, ghostty, …). On terminals without modifier reporting, Shift+Enter degrades to plain Enter (sends) — same as before. Ctrl+J remains the guaranteed-everywhere newline.

若习惯 macOS 的 Shift+Enter 换行，可在 Windows Terminal 的 **settings.json** 里把 Shift+Enter 映射为发送 `\n`（`0x0A`）。在 WT **设置 → 打开 JSON 文件** 编辑，向 `actions` 数组追加：

> **English**: To mirror macOS Shift+Enter, add this to the `actions` array in WT's **settings.json** (**Settings → Open JSON file**). It sends LF (`\n`, hex `0x0A`):

```jsonc
{
  "command": { "action": "sendInput", "input": "\n" },
  "keys": "shift+enter"
}
```

等价写法：`"input": "\u000a"`。保存后 seek 会把收到的 LF 当作 **Ctrl+J** 处理，从而在输入框内换行而非提交。

> **English**: `"input": "\u000a"` is equivalent. After saving, seek treats the LF like **Ctrl+J** — newline in the input box, not submit.

也可在 **设置 → 操作 → 添加新操作** 里用 UI 完成同样配置（**发送输入** → `\n`，绑定 **Shift+Enter**）。

> **English**: The same can be done in the UI under Settings → Actions (**Send input** → `\n`, bind **Shift+Enter**).

不想自己改 JSON？在 seek 里说一句「帮我把 Windows Terminal 的 Shift+Enter 设成换行」——让 seek 帮你处理就好。

> **English**: Don't want to edit JSON yourself? Tell seek something like *"set up Shift+Enter as newline in Windows Terminal"* and let seek handle it.

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

从 [Releases](https://github.com/whyiyhw/seek/releases/latest) 下载 `seek_*_windows_amd64.zip` 解压到**一个固定的目录**（如 `C:\tools\seek\`），或：

> Download from [Releases](https://github.com/whyiyhw/seek/releases/latest) and extract to a **permanent folder** (e.g. `C:\tools\seek\`), or run:

```powershell
go install github.com/whyiyhw/seek/cmd/seek@latest
```

> `go install` automatically places the binary in `%USERPROFILE%\go\bin\`, which is typically already in PATH.

### 添加 PATH / Add to PATH

ZIP 解压后，需要把 exe 所在目录加入 PATH 才能直接在终端输入 `seek`。最简单的方式：

> After extracting the ZIP, add the directory to your PATH so `seek` works from any terminal. The easiest way:

```powershell
seek -install
```

首次启动 TUI 时，seek 也会询问是否自动添加。

> On first TUI launch, seek also offers to add itself to PATH interactively.

也可以手动添加（将 `C:\tools\seek` 改成你的解压路径）：

> Or add it manually (replace `C:\tools\seek` with your extract path):

```powershell
$path = [Environment]::GetEnvironmentVariable("Path", "User")
$dir = "C:\tools\seek"
$segments = $path -split ';' | Where-Object { $_ -ne '' }
if ($segments -notcontains $dir) {
    [Environment]::SetEnvironmentVariable("Path", ($segments + $dir) -join ';', "User")
}
```

修改后**重启终端**生效。

> Restart your terminal after changing PATH. The `go install` path doesn't need this step.

升级 / Upgrade：`seek -upgrade`

---

## 进阶：定时任务、外部触发、通知 / Cron, triggers, notifications

`seek cron` 在 Windows 上跑通需要走 **Task Scheduler**，且 OS 通知在 v0.6.1 是 no-op。完整跨平台设置（含 `~/.seek/cron/env` API key 注入、Task Scheduler 命令、Windows 通知限制说明）见 **[`guide-cron.md`](./guide-cron.md)**。

> **English**: To run `seek cron` on Windows you need Task Scheduler, and OS notifications are a no-op on v0.6.1. Full cross-platform setup (env injection, Task Scheduler commands, the Windows notification limitation) lives in **[`guide-cron.md`](./guide-cron.md)**.
