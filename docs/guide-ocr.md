# seek 图片 OCR — 离线本地文字识别 / Offline image OCR

seek 是**纯文本模型**，但你在编程中需要截图的场景不少：错误消息截图、设计稿标注、白板草图…… OCR 柱（v7 柱 Q）让 seek 能**离线、本地**读取图片中的文字，无需 VLM、无需网络、不依赖云 API。

> The OCR pillar lets seek read text from images **locally and offline** — no VLM, no network, no cloud API. Just reference an image file in your prompt and seek OCRs it before sending to the model.

设计与决策见 [`docs/prd/feature-image-ocr.md`](prd/feature-image-ocr.md)。

---

## 1. 快速开始 / Quick start

把图片文件路径放在你的 prompt 中，seek 自动识别并 OCR：

```
# 直接在 prompt 中引用图片文件
你：修复这个 `@error.png` 中的错误

    ↓ seek 自动检测图片引用 → OCR → 追加文字块到 prompt

模型看到的：修复这个 `@error.png` 中的错误

[image: error.png — OCR]
TypeError: Cannot read properties of undefined (reading 'map')
    at renderList (components/List.tsx:42)
[/image: error.png]
```

支持的图片格式：`.png`、`.jpg`、`.jpeg`、`.webp`、`.tiff`、`.bmp`、`.heic`、`.gif`

### 用法规则

- **直接写路径**：`error.png`、`docs/design.png`、`./screenshot.jpg`
- **用 `@` 前缀**：`@error.png`（最方便，适合当前目录）
- **相对路径**相对于 seek 的 working directory
- **只认真实文件**——代码中的 `.png` 子字符串不会误触发（有 `os.Stat` 校验）

---

## 2. 平台支持 / Platform support

| 平台 | 默认状态 | OCR 引擎 |
|------|---------|----------|
| **macOS** | ✅ **默认开启** | 内置 `vision_ocr`（Swift + Apple Vision API），首次自动编译 |
| **Linux** | ❌ 默认关闭 | 需配置 `ocr.command`（如 tesseract） |
| **Windows** | ❌ 默认关闭 | 需配置 `ocr.command` |

### macOS：零配置开箱即用

macOS 上 seek 内嵌了一个 Swift 源文件（`go:embed`），**首次遇到图片时自动编译**为 `vision_ocr` 二进制并缓存到 `~/.seek/cache/`。编译后以后零延迟。

```bash
# 验证 OCR 可用（往 prompt 里放一张图片就行）
seek -p "这张 @test.png 里写了什么？"
```

也可以预编译：

```bash
# 手动构建 vision 助手（可选，首次图片引用时会自动做）
bash scripts/build-vision-ocr.sh
```

### Linux / Windows：配置外部引擎

在 `~/.seek/config.json` 中配置 `ocr.command`：

```json
{
  "ocr": {
    "command": "tesseract",
    "languages": "eng+chi_sim",
    "timeout_seconds": 15
  }
}
```

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `ocr.enabled` | bool | macOS: true / 其他: false | 强制开关 |
| `ocr.command` | string | — | OCR 可执行文件路径（图片路径自动追加为最后参数） |
| `ocr.languages` | string | `"zh-Hans,en-US"` | 语言提示（macOS Vision 使用的语言提示；tesseract 用 `+` 拼接） |
| `ocr.timeout_seconds` | number | 15 | 每张图片的超时秒数 |

**关闭 OCR**（macOS 上）：

```json
{
  "ocr": {
    "enabled": false
  }
}
```

---

## 3. 输出格式 / Output format

每个识别的图片在 prompt 末尾追加如下格式：

```
[image: <文件名> — OCR]
<识别的文本内容>
[/image: <文件名>]
```

失败情况：

| 情况 | 输出 |
|------|------|
| OCR 未启用 | `[image: test.png — OCR 未启用：macOS 需先构建 vision 助手；其他平台请设置 ocr.command]` |
| OCR 引擎报错 | `[image: test.png — OCR 失败: <错误信息>]` |
| 图片无文字 | `[image: test.png — OCR 未识别到文字，可能是图形/无文本截图]` |

seek **永不因 OCR 失败而中断对话**——失败信息会出现在模型上下文里，模型会据此回复你。

---

## 4. 高级用法 / Advanced usage

### 多图片引用

一个 prompt 中可以引用多张图片，各自独立 OCR：

```
把 @screenshot1.png 的 UI bug 和 @screenshot2.png 的报错一起修复
```

### 外部引擎（可插拔）

`ocr.command` 支持任意的命令行 OCR 工具——图片路径作为最后一个参数传入：

```json
{
  "ocr": {
    "command": "tesseract --psm 6 -l eng"
  }
}
```

引擎只需满足：`<command> <image_path>` → stdout 输出文字。

---

## 5. 与 seek 架构的关系 / Architecture note

OCR 的注入点位于 `Agent.Prompt` 的统一**预处理器**——`internal/ocr` 不依赖 agent / TUI / provider。这意味着：

- OCR **在所有模式下都可用**：TUI、`-p` inline、`seek acp`、cron 子进程
- 即使 OCR 失败，对话也继续（失败信息作为文本注入）
- 完全离线，不增加 API 调用成本

---

## 6. 故障排查 / Troubleshooting

| 现象 | 原因与解决 |
|------|-----------|
| `OCR 未启用` 提示 | 非 macOS 系统。配置 `ocr.command` 或设置 `ocr.enabled=true` |
| `OCR 失败: exec:` | `ocr.command` 指向的二进制不存在或不可执行 |
| 识别结果乱码 | 语言设置不匹配。调整 `ocr.languages` |
| macOS 上 OCR 很慢 | 首次会编译 Swift 源（约 5-10 秒）。之后缓存到 `~/.seek/cache/`，不再编译 |

---

> **下一步**：了解更多 seek 的能力 → [`目录索引`](README.md)
