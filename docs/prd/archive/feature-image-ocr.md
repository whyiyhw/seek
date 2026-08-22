# Feature: 图片输入 → 本地 OCR → 文本（v7 柱 Q）

**所属版本**：v7（v0.8.x）· 柱 Q
**前置阅读**：[`v7.md`](v7.md) §7.4、原始任务书（图片→OCR→注入）
**状态**：🕯 **已下线（2026-08-22 执行）**——被 [`../feature-vision.md`](../feature-vision.md) 取代：DeepSeek 上线原生视觉模型（2026-08-21）后，OCR 的存在理由（文本模型吃不了图）清零。代码已于 feature-vision **M-V.0** 移除（与视觉附加路径同船发布）：`internal/ocr` 整包删除，`DetectImageRefs`/`cleanToken` 迁至 `internal/imgrefs`，`ocr.*` config 键摘除（残留 `ocr` 配置段被静默忽略）。本文归档，以下为交付时的历史记录。✅ **完成**。`internal/ocr`（检测+exec+注入）+ `config.OCRConfig` + print-mode/TUI 接线 + macOS Vision 助手（`internal/ocr/vision_ocr.swift`，**已端到端验证**离线读中英混排）。**打包问题被嵌入方案彻底解决**：`go:embed` 把 Swift 源编进二进制，首次用 OCR 时 `swiftc` 编译并缓存到 `~/.seek/cache/vision_ocr`（`ocr.EnsureVisionHelper` + `Options.Provision` 惰性钩子）——真·单二进制，文档安装路径 `tar -xz seek` 也直接有 OCR，**无需 goreleaser bundle、无需 macOS release runner**。`scripts/build-vision-ocr.sh` 保留（想预编译"挨着二进制"助手者可用，优先级高于 Provision）。真二进制 e2e：删掉预编译助手后 `seek -p "… @img.png"` 触发 Provision→编译→缓存→注入 `[image: … — OCR]`，模型确认读到 "HELLO OCR 123 你好"。新增 12+ 测试 + 嵌入编译 e2e（`SEEK_OCR_E2E` 门控）`-race` 绿。
**估时**：~2-3 天（已基本落地）

**一句话**：seek 模型是纯文本的；本柱让 `seek -p "这个报错怎么修 @err.png"` 在**不联网**下,用本地 OCR 把图转成文字注入 prompt——无 VLM、无网络、保持单二进制（+ 一个可选 70KB 助手）。

---

## 1. 动机
DeepSeek 文本模型不吃图片。用户引用图片（报错截图、文档）时,本地 OCR 成文字注入,是 offline、不破坏 DeepSeek-native 的图片输入路径。**不做** VLM/多模态路由/版面结构化（任务书严格范围）。

## 2. 设计决策（含一处 recon 修正）

### D1 — 挂载点：用户输入处,**不是** `Agent.Prompt`
任务书设想"挂到 @ 引用内联",但 recon 发现 seek **无 @ 内联机制**,且 `.Prompt(ctx, text)` 有 4 个调用点——其中 `main.go:531` 是**子代理** prompt。**挂在 `Prompt` 内会错误地 OCR 子代理 prompt**。所以挂在**用户输入派发处**（print/`-json`/piped 的 `resolvePrompt` 之后,`cmd/seek/main.go`）。TUI 入口是后续。

### D2 — 路径检测 **stat-gated**（头号正确性）
朴素正则扫 `.png` 会误中代码/URL/报错里的字符串。`DetectImageRefs` 只认**扩展名匹配 + `os.Stat` 为真实普通文件**的 token;剥离 `@` 前缀 + 包裹符 + 尾标点。`TestDetect_StatGated_NoFalsePositive` 钉死。

### D3 — 可插拔引擎
解析顺序:`ocr.command`（config / 空格分隔,图片路径追加为末参）→ macOS 随包 `vision_ocr` 助手（seek 二进制旁,`os.Executable()` 定位）。其他平台设 `ocr.command`（tesseract/rapidocr…）。

### D4 — 优雅降级（永不崩 / 永不阻断）
缺引擎 → `[image: x.png — OCR 未启用：…]`;超时/失败 → `[image: x.png — OCR 失败: …]`;空结果 → `[… 未识别到文字,可能是图形/无文本截图]`。`Expand` 永不返回 error。

### D5 — 注入格式（保留原文,追加块）
```
<原始用户输入>

[image: err.png — OCR]
<识别文字>
[/image: err.png]
```
绝不把图片原始字节塞进 prompt。

## 3. 实现
- `internal/ocr/ocr.go`：`Expand` / `DetectImageRefs` / `Run`（os/exec + 15s 超时 + `SEEK_OCR_LANGUAGES` 透传）。
- `internal/config`：`OCRConfig{Enabled,Command,Languages,TimeoutSeconds}` + `OCRCommand/OCRLanguages/OCRTimeout/OCREnabledOr`（macOS 默认 on,其他默认 off 除非设 command）。
- `cmd/seek/main.go`：`ocrOptions(cfg)` + print 派发处 `text = ocr.Expand(...)`。
- `internal/ocr/vision_ocr.swift`：Vision 助手（`.accurate` + zh-Hans/en-US,env 可覆盖语言）;仅系统框架,~70KB。**由 `go:embed` 编进二进制**，`ocr.EnsureVisionHelper` 首次用时 `swiftc` 编译并缓存（`scripts/build-vision-ocr.sh` 仍可手动预编译）。
- `scripts/build-vision-ocr.sh`：`swiftc -O` 构建,非 macOS no-op,bash 3.2 兼容。

## 4. 测试
`internal/ocr`：stat-gated 无假阳性、真实文件检测、@/包裹/标点剥离、去重、fake-command Run、超时、无引擎降级、空 OCR、注入格式、无图不变。`config`：OCR 访问器。**端到端**：本机 `swiftc` 编译助手 + 对生成图 OCR 出 "HELLO OCR 123 你好"（中英数,离线）。

## 5. 验收对照
- ✅ macOS 不联网读截图文字（助手已验证）。
- ✅ 未配置/非 macOS 不崩,给启用指引（降级分支测过）。
- ✅ 无 MCP/VLM/网络;单二进制 + 可选助手。
- ✅ 改动集中:输入派发一处 + `internal/ocr` + Swift 助手 + 配置项。

## 6. 剩余（未做）
- **goreleaser 打包**:darwin archive 里带 `vision_ocr`——需 macOS release runner 跑 `scripts/build-vision-ocr.sh` 后把产物加入 `archives.files`。需真实 release 验证,故未改 `.goreleaser.yml`。
- **TUI 接线**:在 TUI 提交处也 `ocr.Expand`（验收路径是 `-p`,TUI 为增强）。
- **其他平台默认引擎**:仅文档引导设 `ocr.command`,不内置 tesseract 等。
