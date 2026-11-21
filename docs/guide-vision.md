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
- **非视觉模型遇图**：不发图（API 会 400），改为注入一条 in-band 提示，请你用 `/model` 选一个支持视觉的模型。能力判定见下文（`internal/modelcap`），不是"只有 DeepSeek 能看图"。
- **资产库**：提交时图片被复制到 `~/.seek/projects/<id>/assets/`（内容寻址，相同图片自动去重）。session JSONL 只存引用，不膨胀；`-resume` 后历史图片照常重发。
- **前缀缓存**：同一图片每轮重发的字节稳定，可正常命中缓存；每图最多计 384 prompt token。
- **限制**：单图 ≤ 32 MiB；JPEG/PNG/GIF/WebP；图片只进 user 消息。
- **回放**：图片消息渲染为折叠标记 `[image: 名字 · 尺寸 · 大小]`（暗色），原文路径保留在 transcript 里，可自行用看图器打开。

## 二线 provider（Anthropic / OpenAI / Gemini / GLM / OpenCode Go）

**图片照常透传**（feature-vision M-V.4，2026-11-19 交付）。每个 adapter 用自己协议的编码：

- OpenAI 兼容车道（含 GLM、OpenCode Go）：数组形 `content`，`{"type":"image_url","image_url":{"url":"data:…"}}`；纯文本消息仍是纯字符串，字节与改动前一致。
- Anthropic：`{"type":"image","source":{"type":"base64","media_type":…,"data":…}}` block。
- Gemini：`{"inlineData":{"mimeType":…,"data":…}}` part。

**能力判定只有一处**：`internal/modelcap.Vision(model)`。两层来源——

1. 编译期静态表：各车道的默认模型（`deepseek-flash` / OpenCode 的 `deepseek-v4.1-flash` / `claude-*` / `gpt-4o*` / `gemini-*`）。
2. 运行时 grant：OpenCode Go 的目录刷新（`internal/opencatalog`）按网关自己声明的 `modalities.input` 装进来——所以该车道里任何标了 `vision` 的模型（`kimi-k3`、`glm-5.3-flash`、`hy4`…）开箱可用，不用等 seek 发版。grant 只做**升级**不做降级（旧缓存里根本没有这个字段，缺席不能当成"不支持"）。

未知模型按"不支持"处理（宁可一条 in-band 提示，也不要一个 400 毁掉整回合）。让某个自定义 id 收图：若它在 OpenCode 目录里，刷新即生效；否则等静态表补行。

**文件**（PDF）同样透传（2026-11-19，按图片先例）：二线车道的三份官方文档都只收 PDF（OpenAI 兼容 `file` 块 / Anthropic `document` 块 / Gemini `inlineData`），所以 Word/PPT 在二线车道提交期就降级成 `[file: … 仅支持 PDF]` 提示；DeepSeek 车道仍走 Files API，三种格式都收。详见 [guide-files.md](guide-files.md)。
