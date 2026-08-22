# Feature: 原生视觉输入 —— DeepSeek V4-Flash-Vision 接入（柱 Q 管道改道）

**所属版本**：v0.9.x（暂定）· 柱 Q 输入管道的原生视觉延伸
**前置阅读**：[`feature-image-ocr.md`](feature-image-ocr.md)（柱 Q —— 本特性迁移复用其采集/检测管道，并下线其 OCR 出口）、[`feature-image-paste.md`](feature-image-paste.md)（贴图 → 临时 PNG → `@引用`，已交付 .1/.2/.3 核心）、[`vision.md`](vision.md)（北向星 §视觉闭环）、AGENTS.md「Token & prefix-cache constraints」
**状态**：🚀 **已交付（2026-08-22）**。M-V.0 ~ M-V.3 全部实施，`go vet` + `go test ./...` 全绿，真 API smoke 双向验证（vision 模型读出图中文字 "SEEK VISION 42"；非 vision 模型收到切模型提示并转告用户）。§五 各里程碑标 ✅ + 落地处；M-V.4（Files API / 二线透传 / 工具产图）为后续储备。
**触发起因**：DeepSeek 于 **2026-08-21** 发布 `deepseek-v4-flash-vision-exp`——DeepSeek API 平台首个 agent 级视觉模型（文本能力对齐 V4-Flash，Terminal Bench 2.1 **83.9**，多模态 agent 接近 Opus-4.8；每图封顶 384 token 按 Flash 价计）。官方公告 [news260821](https://api-docs.deepseek.com/news/news260821/)。
**估时**：M-V.0 ~ M-V.3 ≈ **2.5–4 天**（含柱 Q 下线）；M-V.4 后续储备。

**一句话**：柱 Q 铺好的图片采集管道（`@引用` + Ctrl+V 贴图 + stat-gated 检测）出口**唯一化**——图片字节作为 content 分块直发视觉模型，非视觉模型遇图提示切模型；柱 Q（OCR 转文字）随之**整体下线**（M-V.0）。**采集层零新建，旧出口整体拔除。**

---

## 一、动机：柱 Q 不变量的前提变了，OCR 一并下线

柱 Q 与 image-paste 立过一条 load-bearing 不变量：

> **只把 OCR 文字注入 prompt，绝不把图片字节塞给模型**（"不破身份"，feature-image-paste §1）。

它的**前提**是"DeepSeek 文本模型不吃图"。2026-08-21 起这个前提不再恒真：`deepseek-v4-flash-vision-exp` 原生接受 `content` 数组分块（`image_url` 形态，base64 data URL / 外链皆可）。

前提既失，**OCR 的存在理由清零**（2026-08-22 决策，详见 §7.1）：它当年解决的唯一真问题是"文本模型吃不了图"；"离线"是伪需求——agent 本身离不开 API；二线 provider（Anthropic / OpenAI / Gemini）亦全有视觉模型。新的出口规则：

| 活动模型 | 图片出口 | 依据 |
|---|---|---|
| 视觉模型（`deepseek-v4-flash-vision-exp`） | **原生字节附加**（content 分块） | 模型直接看图；OCR 丢掉版面/图形/颜色信息 |
| 任何非视觉模型（V4-Flash / V4-Pro / 二线 provider） | **in-band note** 提示 `/model` 切视觉模型 | API 对图片直接 400，门必须在客户端 |

**纪律不变**：非视觉模型依然绝不收图——只是从"OCR 绕行"改为"明说切模型"。北向星（`vision.md` §视觉闭环）指的第一块拼图就是它。

## 二、复用 vs 新建

柱 Q + image-paste 交付的管道，本特性**全部复用、零改动**：

| 环节 | 现状 | 本特性 |
|---|---|---|
| 剪贴板抓图 | `internal/clipimage`（Ctrl+V → 临时 PNG + `@路径`） | 复用，不动 |
| 引用检测 | `ocr.DetectImageRefs`（`internal/ocr/ocr.go:77`，stat-gated 防假阳性） | **迁移**（M-V.0：与 `cleanToken` 一起迁出 `internal/ocr` 至 `internal/imgrefs`——纯文本+stat 逻辑，与 OCR 零耦合） |
| OCR 展开 | `ocr.Expand` / `ExpandImageData` / Swift 助手 / `Options.Provision` | **整体下线**（M-V.0，见 §7.1） |
| 提交期钩子 | `Options.ExpandInput func(string) string`（`internal/tui/model.go:97`，消费点 `update_agent.go:284`）；print 模式 `cmd/seek/main.go:1710`、ACP 贴图 `main.go:2421` 直调 OCR | `ExpandInput` 唯一实现就是 OCR，随柱 Q **删除**——新钩子以正确签名直接接管，无 sibling 包袱（见 D4） |
| 消息结构 | `deepseek.Message.Content string`（`pkg/deepseek/types.go:27`） | **扩展**（见 D1） |

真正的新东西有四块：`pkg/deepseek` 的多模态消息形态（D1/D2）、按模型能力的出口路由（D3/D4）、持久资产库（D5）、**柱 Q 下线**（M-V.0）。

## 三、设计决策

### D1 — `Message` 加兄弟字段，`Content` 保持 `string`（双形态序列化）

不改 `Content any`（那会波及全仓 77 处 string 语境的 `.Content` 读写、29 个文件，且丢类型安全）。改为：

```go
// pkg/deepseek/types.go
type Message struct {
    Role    string      `json:"role"`
    Content string      `json:"content,omitempty"` // 文本部分，语义不变
    // ...
    Images []ImagePart `json:"images,omitempty"` // 会话持久层引用；见 D2
}

type ImagePart struct {
    // 恰好一者非空：
    Asset string `json:"asset,omitempty"` // 持久层：项目资产库文件名（D5）
    URL   string `json:"url,omitempty"`   // 发送层：data: 或 https://（D2 resolve 产物）
}
```

`Message` 加自定义 `MarshalJSON`/`UnmarshalJSON`：

- `Images` 为空 → `content` 序列化为**纯字符串**，与现状**逐字节相同**；
- `Images` 非空 → `content` 序列化为分块数组：`[{"type":"text","text":<Content>}, {"type":"image_url","image_url":{"url":<URL>}}, ...]`。

字节等价不是优化是**契约**：session JSONL 存量兼容（`schema_version=2` 不动，text-only 会话 `-resume` 重放时前缀缓存照常命中），且对齐 `TestCompose_IsDeterministic` 守卫的精神。同理 `StripReasoningContent`（`stream.go:308`）整体结构体拷贝，`Images` 自动幸存，**不碰**。

理由对照仓库惯例：这正是 Sink 接口那条款的镜像——"don't break the main contract, add OPTIONAL sibling"。响应路径零改动（vision 模型输出仍是纯文本，`stream.go` delta `Content string` 不动）。

### D2 — 持久层 vs 发送层切分（镜像 `PredictedNext` 的先例）

`PredictedNext` 已确立此模式：字段留在 `Message` 上便于 session 存储，`PrepareForSend`/`StripReasoningContent` 在**每次发送前**剥离。图片正好反过来——**持久的是引用（`Asset`），发送时才物化（`URL` = base64 data URL）**：

```
提交时:  检测到图 → 复制进资产库（D5）→ Message.Images = [{Asset: "…"}]
发送前:  msgs = deepseek.ResolveImages(msgs, loader)   // Asset → data URL，返回请求副本
         ← 与 StripReasoningContent 同一站、同一拷贝语义
API 侧:  MarshalJSON 输出 image_url 分块
JSONL:   只写 Asset 引用（行不膨胀；32 MiB 的截图不会把每轮全量重写的 session 行撑爆）
```

`ResolveImages` 放 `pkg/deepseek`，loader 由调用方注入（`func(asset string) (dataURL string, err error)`）——`pkg/deepseek` 保持零文件系统依赖、零外部依赖的边界。

### D3 — 能力门是客户端职责：显式清单，不做子串匹配

非视觉模型收到图片 → API 直接 **400**。所以门必须在 seek 侧、发送之前：

```go
// pkg/deepseek
func IsVisionModel(model string) bool // 显式 == 匹配清单
```

显式清单而非 `"vision"` 子串：`-exp` 后缀模型会被 DeepSeek 轮换（types.go:6-15 已记录 alias 移除的教训），隐式模式匹配会静默漂移。路由规则（提交期判断）：

- `IsVisionModel(active)` 且检测到图片 → 附加字节（D4）
- 否则 → in-band note：`[image: <name> — 当前模型不支持图片输入，/model deepseek-v4-flash-vision-exp 切换]`（**永不 error、不阻断会话**）

### D4 — 末端改道：提交期钩子（`ExpandInput` 的继任）

柱 Q 下线后，旧钩子 `func(string) string` 的唯一实现（OCR 展开）一并删除——没有兼容包袱，新钩子直接以正确签名接管：

```go
// internal/tui/model.go Options
ExpandInput func(model, text string) (string, []deepseek.ImagePart)
```

消费点（`update_agent.go:284` 一线，用户消息构造在 `:722`）。钩子收 `model` 参数是因为模型可随 `/model` 中途切换，能力判断必须在**提交时**取当前值。print 模式在 `main.go:1710`、ACP 贴图在 `main.go:2421` 同点分支——**三条入口（TUI / print / ACP）共用同一个路由闭包**，在 `main.go` 组装（那里同时看得见 config 与模型），实现为一个 `visionInputRouter`。

**transcript 自描述标记**：attach 模式下文本尾部追加一行固定标记块 `[image: <name> — attached natively]`（原文照旧保留）。这个字符串会被 replay 渲染，按 AGENTS.md wire-format 纪律：**新增变体必须配 `HasPrefix(out, "[image: ")` 钉死测试**，扩展只能加在闭合 token 之后。

### D5 — 资产库：copy-at-submit，对齐 plan artifact 惯例

剪贴板临时目录（`~/.seek/cache/clipboard/`）是 **GC 区**，不能当持久引用——而 vision 图片**每轮重发**、`-resume` 后还要能找回。参照 `plan/artifact.go` 的 `~/.seek/projects/<id>/plans/` 先例：

- 提交时把检测到的图**复制**到 `~/.seek/projects/<id>/assets/<sha256 前 12 位>.<ext>`（内容寻址，天然去重）；
- `Message.Images[].Asset` 存该文件名；`ResolveImages` 的 loader 从此目录读；
- 资产文件缺失（用户手删）→ 降级为 in-band 错误块（永不 error 哲学），**不 fatal、不阻断会话**；
- 清理策略与 session 生命周期一致（session 删除时目录级回收，M-V.3 处理）。

### D6 — 二线 provider：首版丢图，透传后置

`pkg/agent/translate.go` `msgsToLLM` 直接取 `m.Content`（文本自动携带），`Images` 首版**丢弃**并追加 in-band note（`[image: … — 当前 provider 不支持]`）。Anthropic/Gemini/OpenAI 各有自己的图片分块格式，透传是 M-V.4 的独立工作，不混进本 PRD 首版。`pkg/llm` 的 `Message` 形态**不动**。

### D7 — 前缀缓存与成本

- base64 data URL 字节稳定（同一 asset 每轮重发逐字节相同）→ 前缀缓存**可命中**；每图 384 token 封顶、Flash 单价，成本可忽略；
- 请求体上限 **48 MiB**（单图 base64/URL 32 MiB、Files API 64 MiB、每请求 600 图）——资产库 loader 侧做大小门（超限 → in-band note 降级）；
- 同日发布的 Files API（免费，`file_id` 复用免重传）**首版不用**：图片 token 的缓存参与对 base64 / `file_id` 两路都未验证，M-V.4 统一实验后视结论切默认，结论无论哪边都记 `docs/pitfalls.md`。见 §7.2。

## 四、API 事实速查（2026-08-21 官方文档）

| 项 | 值 |
|---|---|
| model 字符串 | `deepseek-v4-flash-vision-exp` |
| 端点 | 同一 `https://api.deepseek.com/chat/completions`（`/messages`、Responses API 亦支持） |
| 图片位置 | **仅 user 消息**（system/assistant 放图 → 400） |
| content 形态 | 数组分块：`{"type":"text"}` + `{"type":"image_url","image_url":{"url":…}}`；data URL / 外链 / Files API 三种 |
| 计费 | 每图归一化到 ~800×800 等效像素，**封顶 384 token**，按 V4-Flash 价（图大小无关） |
| 格式/上限 | JPEG/PNG/GIF/WebP（按内容嗅探）；请求体 48 MiB；单图 32 MiB（base64/URL）/ 64 MiB（file_id）；≤600 图 |
| thinking | Flash 级语义：默认非 thinking，`/effort` 显式开（`ShouldEnableThinking` 不加 case，行为与 Flash 一致） |

## 五、阶段交付

### M-V.0 — 柱 Q 下线（~0.5–1 天，**与 M-V.2 同船发布**）✅

> **落地（2026-08-22）**：`internal/ocr` 整包 + `scripts/build-vision-ocr.sh` 删除；`DetectImageRefs`/`cleanToken` 迁 `internal/imgrefs`（4 个检测测试随迁）；`OCRConfig` 及四个方法摘除（残留 `ocr` 段静默忽略——json loader 本就不拒未知键）；main.go 三条 OCR 接线 + `ExpandInput` 旧签名一并移除；PRD 归档。

- 删 `internal/ocr` 整包：`DetectImageRefs` + `cleanToken`（纯文本 + stat 逻辑，与 OCR 零耦合）迁至 `internal/imgrefs` 供 D4 路由复用；Swift 助手（`vision_ocr.swift`）、`scripts/build-vision-ocr.sh`、`ExpandImageData`、`EnsureVisionHelper` 一并移除
- 接线摘除：`main.go` 三条 OCR 路径（print `:1710`、TUI expander/provision `:2307-2342`、ACP 贴图 `:2421`）、`internal/tui` 的 `Options.ExpandInput`（唯一实现即 OCR）、`acp_test.go` 相应断言
- config：删 `OCR *OCRConfig` 字段与 `OCREnabledOr`——**加载须容忍旧 config 残留的 `ocr` 段**（忽略不报错，不 fatal）
- 贴图管道（`internal/clipimage` + `@路径` 标记）保留，出口改喂 M-V.2 视觉附加——**先立新出口、再拔旧管道，不留无出口窗口**
- 文档：`feature-image-ocr.md` 移 `archive/`（附取代说明）；`feature-image-paste.md` 状态增注
- 验收：非视觉模型下贴图/`@img.png` → in-band 切模型提示、不 crash；带 `ocr` 段旧 config 加载不破；`go vet` + `go test ./...` 绿

### M-V.1 — `pkg/deepseek` 基座（~半天）✅

> **落地（2026-08-22）**：`pkg/deepseek/vision.go`——`ImagePart`、`IsVisionModel`、`ResolveImages`（含 in-band 降级 note）、`ChatRequest.MarshalJSON` 双形态、`Message.UnmarshalJSON` 双形态兼容、`WithoutImages`。字节等价由 `TestMessageWire_TextOnly_Bytes` / `TestMessage_SessionForm_Bytes` 钉死；pricing 表 + `/model` picker 各加一行。

- `types.go`：`ModelV4FlashVisionExp` 常量、`ImagePart`、`Message.Images`、双形态 `MarshalJSON`/`UnmarshalJSON`、`ResolveImages`、`IsVisionModel`
- 接线：`internal/pricing/pricing.go:52` 费率表加行（Flash 同价）；`internal/tui/commands.go:399` `/model` picker 加行（标注 experimental）
- 验收：httptest fake 断言带图请求体 content 数组形态；**text-only 请求与现状逐字节等价 guard 测试**

### M-V.2 — 提交改道 + 资产库（~1 天）✅

> **落地（2026-08-22）**：`internal/assets`（sha256 内容寻址、原子写、遍历防护、32 MiB 门）；`cmd/seek/visioninput.go` 的 `visionRouter` 三入口共用（TUI `ExpandInput` 新签名 / print `-p` / ACP `buildACPPromptText` 重写）；`Agent.Prompt` 变长参数 + `Config.ImageLoader` 在 `StripReasoningContent` 同站解析；`msgsToLLM` 丢图 + note。**真 API smoke**：vision 模型读出图中 "SEEK VISION 42"；非 vision 模型收到切模型提示并主动转告用户。

- 新 `ExpandInput` 钩子（D4）+ `update_agent.go` 消费 + print / ACP 分支 + `visionInputRouter`
- 资产库写入/copy-at-submit/内容寻址 + `ResolveImages` 进发送路径（与 `StripReasoningContent` 同站）
- 验收：真 API smoke（`.env` key）——贴图 + `/model deepseek-v4-flash-vision-exp`，模型能描述截图内容

### M-V.3 — 回放、resume 与生命周期（~半天）✅

> **落地（2026-08-22）**：`dimImageMarkers` 挂在 `renderUserBlock`（live/scrollback/replay 一条规则，OCR 旧标记同染）；session JSONL round-trip 带 Images（`TestSaveLoad_Roundtrip_Images`）；资产缺失 in-band 降级（`TestPrompt_MissingAsset_DegradesInBand`）；`/compact` 侧信道 `WithoutImages`（pitfall 已记）；**发送路径能力门**——非视觉模型时历史图整批 `WithoutImages`（中途 `/model` 切换不再 400 整回合，`TestPrompt_NonVisionModel_StripsHistoryImages`）。
>
> **resume 真 API 验收**：带图会话存档 → 新进程 `--resume` 追问图中数字 → 模型答 "42"，缓存命中 49.2%（**顺带证实 base64 图参与前缀缓存**，§7.2 实验的一半数据）。验收过程中挖出并修复 seek 存量 bug：`-resume` 的粘性模型继承是死代码（`*model == modelDefault` vs flag 默认 `""` 永不相等），所有会话 resume 后都被静默降级到默认模型——视觉会话因此报 "This model does not support image"，V4-Pro 会话则静默丢失 thinking。已修 + 记 pitfalls。
>
> **偏差记录（D5 资产回收）**：PRD 设想"session 删除时目录级回收"，但 seek **没有 session 删除命令**（用户手动删文件），无挂载点；且资产库是**项目级**、内容寻址、跨会话去重，本就应比单个会话活得久。回收策略改为：手动清理 `~/.seek/projects/<id>/assets/` 即目录级删除；若将来出现 session 删除命令，再挂"扫描剩余会话引用、清除未引用资产"的 GC。

- `internal/tui/replay.go:50` 渲染折叠标记（§7.3 方案 A：`📋 image · <name> · <WxH> · <size>`，尺寸用 stdlib `image.DecodeConfig` 读头部）；`-resume` 后带 `Images` 消息重发 round-trip
- 资产缺失降级（in-band，不 fatal）；session 删除时的资产回收
- 验收：带图 session 存档→重开→继续对话，缓存命中率不塌（`/stats` 观察）

### M-V.4 — 后续储备（不在本 PRD 首版估时内）

Files API 切换（§7.2 A/B 实验后视结论）、二线 provider 图片透传、工具产图路径（screenshot / webfetch 图档直读）。

## 六、测试计划（对照 AGENTS.md failure-mode 标准）

| 类别 | 用例 |
|---|---|
| 序列化 | 双形态 marshal round-trip；**text-only 字节等价 guard**（旧 session 兼容 + 前缀缓存的双保险） |
| 畸形输入 | `Images` 全空字段 part；API 400（`APIError` 路径）；超大资产（>32 MiB）降级为 in-band note |
| 持久化 | 带 `Images` 消息 JSONL save/load round-trip；旧 schema 会话加载不破；resume 后 `ResolveImages` 找回 asset；**asset 文件缺失 → in-band 降级不 fatal** |
| 中断/恢复 | 提交后发送前取消（图片已入库、消息未发——下次 resume 幂等） |
| 翻译层 | `msgsToLLM` 丢图不崩 + note 注入 |
| 下线回归（M-V.0） | 旧 config 带 `ocr` 段加载不破；非视觉模型 + `@img.png` → note 不 crash；`internal/ocr` 删除后全仓 vet/test 绿 |
| 并发 | 无新增共享可变状态（`Images` 值拷贝），`-race` 照常 |
| e2e | 真 API smoke 门控（对齐现有 e2e 惯例，`DEEPSEEK_API_KEY` from `.env`） |

## 七、决策记录与待决事项

### 7.1 已决（2026-08-22，与维护者对齐）

**柱 Q（OCR）彻底下线。** 当天早些时候曾决定"OCR 收敛为非视觉模型的降级路径"，**同日推翻**：OCR 的存在理由（文本模型吃不了图）随视觉模型上线清零——"离线"是伪需求（agent 本身离不开 API），二线 provider 亦全有视觉模型。执行排 M-V.0，与 M-V.2 同船发布（先立新出口再拔旧管道）。随之，任何"OCR 与原图共注入"的 config（如 `vision.ocr_fallback`）一并不做。

**`-exp` 轮换不做预防性设计。** GA 改名时顺手改常量 + `IsVisionModel` 清单 + picker/pricing 各一行即可，改动面本来就小。types.go 的 alias 教训针对的是**服务端静默路由**（reasoner 被降级到 Flash），显式 ID 清单不存在该问题——ID 失效是响亮的 400，不是静默漂移。

### 7.2 已决（2026-08-22）· Files API 与前缀缓存：base64 首发，实验后视结论切

> **决策**：M-V.2 用 base64 内联上线（缓存路径最接近构造性保证）；M-V.4 做 A/B 实验后视结论切 `file_id`。实验**两路都测**——严格说 base64 的"图片 token 可缓存"同样未验证过。

问题本质：**图片 token 参不参与前缀缓存，`file_id` 与 base64 是否等价**——文档对两者都未说明。

为什么值得认真对：不是 384 token 本身（Flash 价下可忽略），而是前缀缓存是**全前缀匹配**——图片通常在历史早期，若图片 token 不进缓存，**其后所有历史每轮全 miss**（"毒化前缀"）：一个 200K token 的长 agent 会话整体按 miss 价计费（~10× 成本）。反过来说，即便命中，收益也是全历史级别的。

已知与未知：

- base64 内联：请求字节逐轮稳定 → 归一化 token 序列稳定，但**图片 token 是否入缓存本身也未验证**；
- `file_id`：请求字节同样稳定（同一 id 字符串），额外多一层未知——服务端是先解析文件再查缓存，还是干脆按 id 缓存（若后者反而更优）。

实验设计（不依赖任何 seek 代码，curl 即可，成本几分钱）：同图同两轮对话，base64 / `file_id` 各跑一路；判据 = 第二请求 `usage.prompt_cache_hit_tokens` ≈ 第一请求 total prompt tokens。seek 侧现成计量：`Usage.HitRatio` / `/stats`。

| 选项 | 内容 | 代价 / 风险 |
|---|---|---|
| ① 先 base64，M-V.4 实验后视结论切 | 缓存路径先用"最接近构造性保证"的一路上线 | 首版带宽浪费：1 MiB 图 ≈ 1.33 MiB/轮上行，50 轮会话 ≈ 66 MB |
| ② M-V.2 动工前先实验定默认 | 4 个请求出数据，PRD 记结论，免得后面返工切默认 | +30 分钟前置；需要 `.env` 真 key |
| ③ 直接 `file_id` | 免重传、请求体最小 | 毒化前缀若成立，长会话成本 ~10× |

兜底（任一选项都成立）：本地资产库（D5）是持久真相源，`file_id` 只是传输优化——文件过期/缺失回落 base64 重传，**决策完全可逆**。另：若实验发现**两路都不缓存**，图片的"历史下线策略"（老图换占位文本，只保最近 K 张）成为 M-V.4+ 的成本阀门。

### 7.3 已决（2026-08-22）· 回放展示：方案 A 折叠标记

> **决策**：折叠标记进 M-V.3 验收；C（OS 查看器）**不做**——真要看原图，`@路径` 引用本身就在 transcript 里；B（终端图形协议）明确排除。

| 方案 | 内容 | 评估 |
|---|---|---|
| **A 折叠标记** | `📋 image · shot.png · 1024×768 · 214 KB`——与现有 `📋 pasted N lines` 同一惯用语；尺寸/大小用 stdlib `image.DecodeConfig` 读头部（零依赖） | 全终端 / SSH / 哑终端兼容；live 与 replay 同一条规则（样式化 `[image: ` 前缀行，与 OCR 注入块同族，**一条样式规则覆盖两代**）；replay 零额外状态，对齐"从 transcript 重建"纪律 |
| B 终端图形协议 | Kitty graphics / iTerm2 / Sixel 渲染真缩略图 | 支持矩阵碎片化（Windows Terminal 仅 Sixel 且照片效果差，Kitty/iTerm2 各自为政）；引入终端探测逻辑甚至依赖；违背单二进制气质。**建议排除** |
| C OS 查看器加餐 | 标记上按键 / `/view <n>` 调系统看图器（`start` / `open` / `xdg-open`） | 真要看原图时才有用——但 `@路径` 引用本就留在 transcript 里，用户可自行打开 |

---

**与北向星的关系**：`vision.md` 三层架构里，本特性是 CLI 层吃上图的第一口——它验证"模型原生看图"的价值，为系统应用层的视觉采集（截屏流、UI 元素树）铺好消费端。
