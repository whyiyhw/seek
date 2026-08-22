# Feature: 粘贴图片进 TUI → 本地 OCR（v0.8.x · 柱 Q 的延伸）

**所属版本**：v0.8.x（构建在 v7 柱 Q OCR 之上）
**前置阅读**：[`feature-image-ocr.md`](feature-image-ocr.md)（柱 Q —— 本特性复用它的整条 OCR 管道）、`internal/tui/paste.go`（现有 `Ctrl+V` 粘贴 + 折叠标记机制，本特性镜像它）
**状态**：🚧 **M-imgpaste.1 + .2 已交付，.3 核心已验**。**2026-08-22 增注**：采集管道（clipimage 抓图 + `@路径` 标记）**保留**，出口从 OCR 改喂原生视觉（[`feature-vision.md`](feature-vision.md) M-V.0/M-V.2 同船）——本文中"走现有 OCR 管道"的下游引用届时失效，仅 Linux/Windows 抓图器验证等采集侧尾巴仍有意义。.1：`internal/clipimage` 可插拔抓图器（命令/平台默认 + no-image/no-grabber 检测 + 临时文件 GC），纯单测 82.5%。.2：TUI 接线 —— `Ctrl+V` 探到图 → `clipimage.Grab` → 折叠标记 `📋 image` → 提交时 `resolvePasteInInput` 换成 `@<临时PNG>` → 走现有 OCR 管道;无图降级文本粘贴。`Options.GrabImage` 注入,env `SEEK_CLIPBOARD_IMAGE_CMD` 可覆盖。.3：**macOS osascript 抓图器真机验证通过**（剪贴板放图→Grab 取回 21KB PNG）。全仓 vet+race 绿。剩 .3 尾巴：config `imagepaste.command` key（env 已通）、Linux/Windows 真机验证（非 macOS 上做）、真 TUI 手动 e2e。
**估时**：~2-3 天

**一句话**：让用户在 TUI 里**直接 `Ctrl+V` 粘贴剪贴板里的图片**（截图 / Preview 里 Copy 的图）——seek 把它落成临时 PNG、插入一个 `@路径` 引用，提交时走**已有的柱 Q OCR 管道**转成文字注入 prompt。**新代码只有"从剪贴板抓图到临时文件"这一步**;其余全复用。

---

## 1. 动机 / 为什么这是个小特性

柱 Q 已经能 OCR **文件路径**引用（`@err.png`）。但用户的真实动作往往是"截个图直接粘"——目前粘不进去,因为 TUI 的 `Ctrl+V`（`tryClipboardPaste` → `atotto/clipboard.ReadAll`）**只读文本剪贴板**,读不到 macOS NSPasteboard 上的图片字节。

关键认知:**粘贴管道早就在了**——`Ctrl+V`、bracketed-paste、多行折叠标记(`📋 pasted N lines`)都有。缺口很窄:剪贴板里是图片时,多走一条"抓图→临时文件"的路,然后**接回柱 Q 现成的 OCR 管道**。所以这不是从零做图片输入,是给现有粘贴 + OCR 之间补一段桥。

**不破身份**:仍然**只把 OCR 文字注入 prompt,绝不把图片字节塞给模型**(柱 Q 的 load-bearing 不变量)。DeepSeek 文本模型不吃图,这条不松。

---

## 2. 设计

### 2.1 流程（粘贴 → 临时文件 → 复用 OCR）
```
用户 Ctrl+V
  └─ tryClipboardPaste（扩展）:
       1. 先探剪贴板有没有图片(grabber 探测)
       2a. 有图 → 抓到临时 PNG（~/.seek/cache/clipboard/<ts>.png）
              → 存 pastedImagePath，往 textarea 插一个折叠标记
                「📋 image — 提交时 OCR」（镜像现有 pastedContent 折叠）
              → 返回（不再走文本粘贴）
       2b. 没图 → 落回现有文本粘贴路径（行为不变）
提交时（resolvePasteInInput 镜像）:
  └─ 把图片折叠标记替换成 `@<临时PNG路径>`
       → 现有 ExpandInput / ocr.Expand 检测到真实图片文件
       → 走柱 Q 的 Vision 助手 OCR → 注入 `[image: … — OCR]` 文本块
```

**复用清单**(这就是为什么小):
- `Ctrl+V` 触发点 = 现有 `tryClipboardPaste`(只加一个图片分支)。
- 折叠显示 = 现有 `pastedContent` / `pasteFoldMarker` / `resolvePasteInInput` 机制,新增一个并列的 `pastedImagePath` 字段。
- OCR = **完全复用** `ExpandInput` → `ocr.Expand` → 柱 Q Vision 助手(`@路径` 流),零新 OCR 代码。

### 2.2 唯一的新件:剪贴板抓图器（pluggable）
仿柱 Q 的 `ocr.command` 可插拔思路:一个把"剪贴板图片"写到给定路径的命令,`imagepaste.command`(config)/ `SEEK_CLIPBOARD_IMAGE_CMD`(env)可覆盖。退出非零 / 无输出 = "剪贴板里没有图片" → 落回文本粘贴。

| 平台 | 默认抓图方式 |
|---|---|
| **macOS**（先行） | 复用柱 Q 的嵌入-编译 Vision 助手模式:一个小助手读 `NSPasteboard` 图片写 PNG（或给 `vision_ocr` 加 `--from-clipboard` 模式直接 OCR）。无第三方依赖。 |
| Linux | `wl-paste --type image/png`（Wayland）/ `xclip -selection clipboard -t image/png -o`（X11）—— 文档给出,best-effort |
| Windows | PowerShell `Get-Clipboard -Format Image` —— 文档给出,best-effort |
| 其他 / 无抓图器 | 优雅降级:`Ctrl+V` 行为不变(文本粘贴),提示用户用 `@路径` 流 |

---

## 3. 关键决策（已拍板默认，可改）

| # | 决策 | 选择 | 理由 |
|---|---|---|---|
| D1 | 触发方式 | **扩展现有 `Ctrl+V` 自动判别**(有图抓图,无图走文本) | 零新键位、最自然;`tryClipboardPaste` 已是入口 |
| D2 | 插入什么 | **临时 PNG + `@路径` token**,经现有 折叠→resolve→ExpandInput 管道 | **零新 OCR 代码**;用户看得到引用、可在发送前删/改;惰性(提交时才 OCR) |
| D3 | 平台 | **macOS 先行**(复用 Vision 助手基建),Linux/Windows 走 pluggable 命令、降级 | 与柱 Q 一致;主用户在 macOS |
| D4 | 抓图器 | **pluggable `imagepaste.command`**,macOS 默认嵌入助手 / osascript | 仿 `ocr.command`;无第三方二进制依赖 |

---

## 4. 接口 / 配置

- **`Ctrl+V`**:剪贴板有图 → 抓图 + 折叠标记;无图 → 文本粘贴(现状)。
- **config `imagepaste.command`** / **env `SEEK_CLIPBOARD_IMAGE_CMD`**:覆盖抓图命令(命令把剪贴板图片写到末参给出的路径;无图则非零退出)。
- **临时文件**:`~/.seek/cache/clipboard/<ts>.png`(`paths.Cache()`),best-effort GC(按 age / 会话结束清)。
- 折叠标记文案:`📋 image — 提交时 OCR`(提交替换成 `@<path>`)。

---

## 5. 安全 / 边界

- **绝不把图片字节进 prompt** —— 只注入 OCR 文字(柱 Q 不变量,本特性继承)。
- **降级不崩**:无抓图器 / 剪贴板非图片 / 抓图失败 → 静默落回文本粘贴 + 一次性提示走 `@路径`。
- **临时文件**:有大小上限(超大图拒绝并提示);写到用户级 cache,可清;不进 git。
- **隐私**:抓的图落本地临时文件、本地 OCR,不联网(与柱 Q 一致)。

---

## 6. 不做什么（v1 边界）

- ❌ **TUI 里渲染图片缩略图**(终端不通用;`📋 image` 标记足够)。
- ❌ **把图片原始字节送给模型 / VLM 多模态**(违反柱 Q 身份)。
- ❌ **拖拽(drag-drop)图片进终端**(另一套机制,单列)。
- ❌ **Linux/Windows 抓图的精雕**(v1 给命令 + 文档,macOS 先做扎实)。
- ❌ **多图一次粘**(v1 先支持单图;多图留后续)。

---

## 7. 测试（按 CLAUDE.md「test the failure modes」）

- **图/文判别**:剪贴板有图 → 抓图 + 插标记;有文本 → 走文本粘贴(现状不回归);啥都没有 → no-op。
- **折叠 → resolve**:标记在提交时正确替换成 `@<临时路径>`,且该路径是真实文件(`os.Stat` 通过)。
- **接回 OCR**:提交后 `ExpandInput`/`ocr.Expand` 对临时 PNG 出 `[image: … — OCR]`(复用柱 Q 的假 OCR 引擎测法)。
- **抓图器缺失 / 非图剪贴板 / 抓图非零退出**:降级到文本粘贴,不崩、不误判。
- **抓图器 = 假命令**(写一个已知 PNG)→ 全链路可测,不依赖真剪贴板。
- **临时文件清理**:GC 逻辑(age / 会话结束)。
- **大小上限**:超大图被拒 + 提示。

---

## 8. 里程碑

| 里程碑 | 内容 |
|---|---|
| **M-imgpaste.1** ✅ | `internal/clipimage`:pluggable 抓图器(`Options.Command`/`defaultGrabCommand` 平台默认,把剪贴板图写到末参路径)+ `ErrNoImage`/`ErrNoGrabber` 检测 + `gcOldGrabs`(24h age GC)。纯单测(假命令:has-image/empty/non-zero/no-grabber/canceled/GC),82.5%。 |
| **M-imgpaste.2** ✅ | TUI 接线:`tryClipboardPaste` 加图片分支(超时保护)+ `Options.GrabImage` 注入 + `Model.pastedImagePath` + `imagePasteMarker` 折叠 + `resolvePasteInInput` 换成 `@<路径>`(镜像文本粘贴)。`cmd/seek` 接线(env `SEEK_CLIPBOARD_IMAGE_CMD` 覆盖、cache dir `~/.seek/cache/clipboard`)。假抓图器单测:image→marker、resolve→@path、无图不回归、空 no-op。 |
| **M-imgpaste.3** 🚧 | **macOS osascript 抓图器真机 e2e 通过**(剪贴板放图 → `Grab` 取回 21KB PNG,gated darwin test)+ 临时文件 GC ✅。**剩**:config `imagepaste.command` key（env 已通）、Linux/Windows 真机验证（需在对应平台）、完整 TUI 手动 e2e（`Ctrl+V` 活体粘贴需真终端）。 |

---

## 9. 与现有流的关系

| | `@路径`（柱 Q 现状） | 粘贴图片（本特性） |
|---|---|---|
| 用户动作 | 截图存盘 → 手写 `@~/Desktop/x.png` | 截图进剪贴板 → `Ctrl+V` |
| 中间 | —— | 抓图到临时 PNG → 插 `@临时路径` |
| OCR | `ExpandInput` → 柱 Q | **同一条** `ExpandInput` → 柱 Q |
| 注入 | `[image: … — OCR]` | **同样** `[image: … — OCR]` |

> 本特性 = 给"截图 → 引用"这一步加自动化;OCR/注入完全是柱 Q 的现成轨道。落地后更新 `guide-ocr.md` / `feature-image-ocr.md` 提一句"也支持粘贴"。
