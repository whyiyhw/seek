# Vision — 原生图片输入

seek 支持把图片**原样**发给多模态模型：剪贴板截图、本地图片文件，模型直接"看图"。此前的本地 OCR 路线（柱 Q）已随视觉模型上线整体下线。

## 用法

**默认即视觉**：DeepSeek V4.1 Flash（`deepseek-flash`，2026-09-10 发布）原生多模态——默认模型直接收图，**无需任何 `/model` 切换**。粘贴或引用图片直接发送即可（每图封顶 384 token 计费）。

两种进图方式：

- **粘贴**：TUI 里 `Ctrl+V` 粘贴剪贴板图片（截图 / 复制的图），输入框出现 `📋 image` 标记，直接回车发送。Windows 默认终端（Windows Terminal 等）会把粘贴和弦拦成自己的动作，seek 把随之而来的空粘贴事件当作贴图意图，`Ctrl+V` 原样可用；放行按键的终端里 `Ctrl+V` / `Ctrl+Shift+V` 也都已绑定。
- **引用**：在输入里写已存在的图片路径，`@err.png` 或裸路径均可（stat-gated：只有真实存在的文件会被识别）。

旧会话注意：seek 的模型注册表只认 `deepseek-flash` 一个 id。`-resume` 一个模型为旧名(`deepseek-v4-flash-vision-exp` 等)的历史会话时，该名字按未知模型处理——历史里的图片会被丢弃(in-band 标记保留，transcript 仍自描述)，预算走 128K 保守默认，计价走 Flash 卡(与服务端实际计费一致)。想恢复图片输入，会话里执行 `/model deepseek-flash` 即可。

## 行为细节

- **抓图器（平台默认）**：macOS 走 `osascript`；Linux 走 `wl-paste`（Wayland）/ `xclip`（X11）；**Windows 走 Windows PowerShell**（WinForms 读剪贴板位图，实测约 0.3 s）。要换抓图器就设 `SEEK_CLIPBOARD_IMAGE_CMD` —— 值按空白拆分成参数，所以参数里（含脚本路径）别带空格。
- **非视觉模型遇图**：不发图（API 会 400），改为注入一条 in-band 提示，模型会告诉你切换 `/model deepseek-flash`。典型场景是二线 provider（Anthropic / OpenAI / Gemini）——vision 仅 DeepSeek Flash 档支持。
- **资产库**：提交时图片被复制到 `~/.seek/projects/<id>/assets/`（内容寻址，相同图片自动去重）。session JSONL 只存引用，不膨胀；`-resume` 后历史图片照常重发。
- **前缀缓存**：同一图片每轮重发的字节稳定，可正常命中缓存；每图最多计 384 prompt token。
- **限制**：单图 ≤ 32 MiB；JPEG/PNG/GIF/WebP；图片只进 user 消息。
- **回放**：图片消息渲染为折叠标记 `[image: 名字 · 尺寸 · 大小]`（暗色），原文路径保留在 transcript 里，可自行用看图器打开。

## 二线 provider（Anthropic / OpenAI / Gemini）

首版不透传图片：带图消息会附一条"未透传"提示。per-provider 图片格式透传是后续工作（feature-vision M-V.4）。
