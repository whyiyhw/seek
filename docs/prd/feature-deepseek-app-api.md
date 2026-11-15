# Feature: DeepSeek 全家桶应用 API 接入评估（read_url / 文件输入 / ASR-TTS / 安全校验）

**所属版本**：独立 feature PRD（评估 + 分期设计稿，未排期实现）
**前置阅读**：[`feature-vision.md`](feature-vision.md)（imgrefs 先例——本文②的管道蓝本）、[`feature-remote-control-plane.md`](feature-remote-control-plane.md)（语音 frontend 的宿主——本文③的骨架落点）、[`internal/tools/websearch/websearch.go`](../../internal/tools/websearch/websearch.go)（按次计费工具 + `RecordFlatCost` 先例）、AGENTS.md「Token & prefix-cache constraints」
**状态**：📐 **设计稿（2026-11-13 评估；R0 验证门已执行 2026-11-14 → ① 判红关闭，见 §2.2）**。四项结论见 §0；其余里程碑为 2h 卡粒度拆解，**未动工**。
**触发起因**：DeepSeek 于 **2026-11-13** 发布「全家桶应用 API」——官方公告 [news261113](https://api-docs.deepseek.com/zh-cn/news/news261113)：对话内联网搜索（含深度思考搜索）、`deepseek_read_url` 链接阅读、文件输入（PDF/Office/图片）、模型安全校验，加独立底层接口 Search / Fetch / OCR / ASR / TTS。同族先例：Search API 2026-10-17 开放，seek 已接（`pkg/deepseek/search.go` + `web_search` 工具，另卡实现）。
**本文不覆盖**：独立 Search API 的对话内接线（web_search 服务端工具变体）——与 read_url 同属 `tools` 数组声明，但 seek 已有客户端 `web_search` 工具覆盖显式搜索场景；其服务端变体只在「思考模式自主搜索」里顺带评估（§二）。

> **一句话**：四项里**文件输入接**（照 imgrefs 先例扩 filerefs）、**安全校验接**（config 可选、默认关）、**ASR+TTS 接客户端层**（语音 frontend 分期，v1 出站播报、不做实时双工）、**read_url 不接**（R0 实测红 2026-11-14：与 function tools **无共存形态**——thinking off 混用 400、thinking on 混用 200 但 server tool 被静默剥除，两形态交集为空）；OCR 独立接口**不接**（被文件解析覆盖）。

---

## 0. 结论总表

| # | 项目 | 结论 | 一句话理由 | 首卡（2h 粒度） |
|---|---|---|---|---|
| ① | `deepseek_read_url` + 思考模式自主搜索 | **不接**（R0 红关闭，2026-11-14） | 实测共存性为**假绿**：thinking off + function tools 同请求 400（"client tools can only be used together with … in thinking mode"）；thinking on 返回 200 但 server tool 被静默剥除——模型明说「only tool available to me is a weather lookup」，零抓取零 `server_tool_use`。**两形态交集为空**，agent 主路径（恒带 20+ function tools）不可用；web_search 服务端变体同规则 400（「思考模式自主搜索」一并关闭）。R1–R4 关闭；旁路一次性调用维持记档不排期 | ~~R0 验证卡~~ 已执行（§2.2） |
| ② | 文件输入 PDF/Office | **接** | agent 读 PDF/Office 是高频真需求（现状靠 `pdftotext` 这类不一定装了的 CLI 绕行）；管道与 imgrefs 先例逐点同构（copy-at-submit + 提交期路由 + 永不 error）；计费清晰（解析按页 + token） | F1 `pkg/deepseek` files 客户端（§三） |
| ③ | OCR / ASR / TTS 独立接口 | OCR **不接**；ASR+TTS **接（客户端层）**，语音 frontend 分期 | OCR 被②的文件解析覆盖（对话面），坐标级提取无 seek 消费场景；ASR+TTS 是 control-plane 语音 frontend 的原料，客户端卡便宜（照 `search.go` 抄）；frontend 本体 v1 只做出站 TTS 播报（收发不对称是 `Frontend` 端口的设计前提），**不做实时双工**（ASR 无流式、TTS 不支持中断续传） | S1 TTS 客户端（§四） |
| ④ | 模型安全校验 `safety_check` | **接**（小卡） | 自用默认关（拒答会降级 agent 在安全研究类任务上的能力、徒增延迟），但参数暴露为 config 可选项成本极低，且对外提供服务时有合规义务 | SC1（§五） |

**卡外相邻发现（已收口 2026-11-15，判定者联网核实）**：官方 [en 页](https://api-docs.deepseek.com/quick_start/pricing)（USD，峰谷制）现值 flash **$0.30/$0.006/$1.20** peak（off-peak 对折，served by *DeepSeek-V4.1-Flash-1017*）+ `deepseek-v4-pro` 独立行 **$1.32/$0.044/$3.96** peak（*DeepSeek-V4-Pro-0813*）——与 `internal/pricing/pricing.go` **逐位一致，未过期**。[zh 页](https://api-docs.deepseek.com/zh-cn/quick_start/pricing)已分叉为另一套 flat CNY 价目（**¥1/¥0.02/¥2** flash · **¥3/¥0.025/¥6** pro，无峰谷），跨页比价产生「调价」幻影；本卡初稿引述的「¥1/¥2（空闲/高峰）· ¥4/¥8 output · v4-pro ¥4.5/¥9」系误读，作废。billing 跟随账户区域，pricing.go 以 en 页 USD peak 为锚。

---

## 1. 发布面事实速查（2026-11-13 官方文档，逐项来源标注）

### 1.1 对话内 web 工具（read_url / web_search 服务端变体）

来源：[Web Tools Guide](https://api-docs.deepseek.com/guides/web_tools)、[build_a_chatbot](https://api-docs.deepseek.com/zh-cn/guides/build_a_chatbot)、[Chat Completions API](https://api-docs.deepseek.com/api/create-chat-completion)。

| 项 | 事实 |
|---|---|
| 声明方式 | chat 请求 `tools` 数组加 `{"type":"deepseek_web_search"}` / `{"type":"deepseek_read_url"}`（**无 `function` 字段**——与 function tools 异形）；模型必须是 `deepseek-flash` |
| 行为 | 客户端不执行任何工具，**模型自主决定**是否搜索/读链接；非思考模式 = 普通搜索带引用；思考模式 = 深度思考搜索（**最多约 20 次工具调用**，action 含 search/open_page/find_in_page，此时不再单独做 URL 阅读） |
| read_url 规则 | 自动识别**最后一条 user 消息**里的链接，**至多前 10 个不同 URL**，重复 URL 只算一次 |
| 响应形状 | `content` 无内联引用标记，citations 在 `message.annotations`（`url_citation`，`start_index`/`end_index` 按 Unicode code point）；`server_tool_calls` 扩展字段逐步记录（含 `kind`/`signature`）；`reasoning_content` 只保留最后一步思考 |
| 流式 | 思考/文本照常走 delta；工具进度走 `server_tool_event`（`step_reasoning_done`/`call_started`/`call_completed`）；完整 `server_tool_calls`+`annotations` 在带 `finish_reason` 的末 chunk 一次性下发 |
| **多轮硬约束** | **必须原样回传含 `kind`/`signature` 的内容**，否则「请求仍成功但页面丢失」（模型下一轮看不到已抓内容 → 重复抓取重复计费） |
| 同用限制 | 不能与其他 **server tool** 同声明；同工具声明两次报错；`tool_choice` 不能指定 web 工具（`required` 视为 auto，`none` 连 URL 阅读一起关）；`response_format` 非 text、`logprobs` 返回 400；用过 web 工具的回复不能 continue |
| 采样参数 | web 工具开启时 `temperature`/`top_p` 由服务端决定（seek 的 agent 路径本就不发这两个字段——已核实 `pkg/agent` 无赋值点，非问题） |
| 计费 | 搜索按 query 计费（无结果不收费）；图片搜索每图 1 次 + 模型选中区域再 +1；**抓取按成功页数计费，含被 safety 替换为 `url_not_allowed`/`unavailable` 的**（真抓取失败不收费）；思考搜索里重复打开/页内移动/`find_in_page` 免费；HTTP 200 后中途失败，已跑的推理/搜索/读页照常计费 |

### 1.2 文件输入（Files API + 文件解析）

来源：[Files API](https://api-docs.deepseek.com/zh-cn/guides/files_api)、[文件解析](https://api-docs.deepseek.com/zh-cn/guides/document_parsing)。

| 项 | 事实 |
|---|---|
| 上传 | `POST /files`，multipart，**`purpose` 字段必须放在 `file` 之前**；返回 `file-api-xxxx` 形态 id，**上传时即完成解析**（成功才返回 id） |
| purpose 三值 | `user_data`（图片/纯文本/表格/电子书，**上传不计费**）；`text_recognition`（PDF/Word/PPT 仅文字，图表丢弃）；`rich_layout_recognition`（图文混排，图占输入 token）；**`user_data` 传 PDF/Word/PPT 直接 400** |
| 引用方式 | 仅 `file_id`（不支持直接传内容/URL）；chat/completions 下 user 消息 content 数组加 `{"type":"file","file_id":…}` 分块 |
| 限制 | 文档 100 MiB / 1000 页 / 上传 120s / 解析 120s 超时；每账号同时 15 个文档；每请求 ≤20 个文档、文字合计 ≤8 MiB；同 file_id 多次引用按多次计 |
| 生命周期 | 有效期 1h–30 天或永久；**原始文件与解析结果均不支持下载**；25 GiB/用户、1 万文件 |
| 计费 | **解析成功时按页一次性计费**（¥1.5/千页）；对话引用不再收解析费，按拼入上下文的输入 token 计 |

### 1.3 独立底层接口（OCR / ASR / TTS）

来源：[OCR](https://api-docs.deepseek.com/zh-cn/guides/ocr)、[ASR](https://api-docs.deepseek.com/zh-cn/guides/asr)、[TTS](https://api-docs.deepseek.com/zh-cn/guides/tts)。

| 接口 | 端点 | 形态 | 关键约束 | 计费 |
|---|---|---|---|---|
| OCR | `POST /v1/tools/ocr` | multipart 直传（**不走 Files API、不存储、不交对话模型**） | 100 MiB / 1000 页；逐页返回（含 `<｜ref｜>` 坐标标记，归一化 0–1000）；`finish_reason:length` = 该页文本不完整 | ¥1.5/千页，按成功页数 |
| ASR | `POST /v1/tools/asr` | multipart 音频 | **非流式**（一次传完整音频）；16 位 PCM WAV 或 Ogg Opus；≤8 MiB、≤120 秒；21 语种自动识别 | ¥0.78/小时（**精确到秒、不足 1 秒按 1 秒**）；限时 5 折 ¥0.39 |
| TTS | `POST /v1/tools/tts`（HTTP 整段）/ `wss://…/tts`（流式分段） | JSON `{"text":…,"voice_id":…}`；音频流回 | ≤10,000 计费字符（**汉字算 2 字符**）、请求体 64 KiB；音色 stella（默认）/tide；wav/pcm（24 kHz 16 位单声道）/opus（仅 WS）；**不支持语速调节、不支持中断续传**；Markdown 过滤默认开 | ¥3/万字符；限时 5 折 ¥1.5 |

### 1.4 模型安全校验

来源：[build_a_chatbot](https://api-docs.deepseek.com/zh-cn/guides/build_a_chatbot)。

- 参数：`"safety_check": {"level": "high"}`；命中风险返回**拒答标识**（`stop_reason: "refusal"`，客户端需用拒答文案替换已展示的回答）。
- 官方立场：对外提供服务时**务必开启**并自担内容安全审核、大模型登记、AI 标识等义务；价格页**未列出独立收费条目**（按无额外费用处理，实现卡里顺手核实账单）。

### 1.5 工具费与 token 费的叠加口径（官方原文归纳）

> 「模型调用费用 = token 消耗量 × 模型单价；搜索（文字、图片）和网页抓取按上表价格按次计费」——**token 费与按次工具费分开计算、分别扣减**，赠送余额优先扣。

seek 侧落地口径（§六详列）：token 费照旧走 `pricing.Cost`（峰谷折价照旧）；工具费是**平价项**（per-call/per-page/per-char/per-second），走 `RecordFlatCost` 家族，**不吃峰谷折价**（工具价页无峰谷之分）。

---

## 2. ① `deepseek_read_url` + 思考模式自主搜索 —— **不接（R0 验证门红，2026-11-14）**

### 2.1 价值与现状差分

seek 现有联网三件套：`web_search`（客户端工具，模型显式调用，按次计费已进 status bar）+ `webfetch`（本地 HTTPS GET + SSRF 门）+ 搜索结果摘要。read_url / 服务端 web_search 的真增量有三：

1. **思考模式自主搜索**：thinking 开启时模型在多步推理中自己决定搜什么、读哪页（~20 次调用），这是客户端工具做不到的（function call 只发生在思考结束后的输出阶段）。
2. **服务端抓取零 SSRF**：与 `web_search` 同理——模型控制的 URL 由 DeepSeek 侧抓，seek 无本地 fetch 面。
3. 服务端渲染管线（JS 页面）大概率优于 seek 的裸 HTTPS GET。

### 2.2 R0 验证实测（2026-11-14，真 API curl）—— **红：假绿**

**结论：function tools 与 read_url 无共存形态。** thinking off 混用直接 400；thinking on 混用返回 200 但 **server tool 被静默剥除**（不报错、不抓取、不计费、模型工具清单里根本没有它）。「200」是假绿——比干脆的 400 更危险，因为不实测到「模型说没这工具」那一步根本发现不了。seek agent 主路径（`agent.go:776` 每请求恒带 20+ function tools）不可用 → **①降级为不接，R1–R4 关闭**；web_search 服务端变体实测同规则 400（t7），「思考模式自主搜索」增量一并关闭。

**10 次真 API 调用的证据表**（t1–t10；其中 t1/t7 为 400 不计费，8 次生成。探针 URL 用 example.com 与 httpbin.org/uuid——后者每次抓取返回新 UUID，可排除「模型凭记忆作答」的假象）：

| # | 形态 | thinking | 结果 |
|---|---|---|---|
| t1 | function + read_url | off | **400**：`tools: client tools can only be used together with \`deepseek_web_search\`, \`deepseek_read_url\`, \`safety_check\` or \`title\` in thinking mode` |
| t2 | function + read_url | on | 200 **假绿**：无 server_tool_calls、无 server_tool_use，模型答「I don't have a tool to fetch or browse web pages」 |
| t3 | read_url 单独 | off | 200 真抓取 ✓ server_tool_calls + signature（1904b），`tool_fetch:1` |
| t4 | read_url 单独 | on | 200 未抓（example.com 太著名，模型裁量跳过——见 t5 排除） |
| t5 | read_url 单独 | on | 200 **真抓取** ✓ 逐字返回新鲜 UUID，`tool_fetch:1`——thinking 本身不禁用 read_url |
| t6 | function + read_url | on | 200 **假绿定谳**：不可知 UUID URL 也零抓取，模型明说「the only tool available to me is a weather lookup」——server tool 被剥除而非模型裁量 |
| t7 | function + web_search | off | **400 同规则**——web_search 变体同样不能与 function tools 共存 |
| t8 | read_url 单独（turn 1） | off | 200 抓取 ✓ signature 220b 捕获（注意：返回的 UUID 与 t5 相同 → **服务端有页面缓存**，但重复抓取照计费） |
| t9 | turn 2 **回传** server_tool_calls | off | 200 **零新抓取**（usage 无 server_tool_use 键），signature 服务端还原页面，答案正确 |
| t10 | turn 2 **丢弃** server_tool_calls | off | 200 **重复抓取并计费**（`tool_fetch:1`），页面重新注入（prompt 250 vs t9 的 160） |

**② 字段实证（chat/completions 非流式，两 thinking 形态）**——`server_tool_use` 只在 server tool 有活动时出现（全零则整个键缺席，t2/t6/t9 均无此键）；展示计数 `web_search_requests`/`web_fetch_requests` 在本次全部非流式响应中均未出现（文档口径它们存在，疑似 omitempty）：

```json
// usage（t3，thinking off，抓取 1 页）——注意新旧两族字段并存
{"prompt_tokens":316,"completion_tokens":11,"total_tokens":327,
 "prompt_tokens_details":{"cached_tokens":0},
 "prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":316,
 "server_tool_use":{"tool_search":0,"tool_fetch":1,"tool_image_search":0}}

// message.server_tool_calls（t3；thinking on 的 t5 同形 + 多 reasoning_content）
[{"index":0,"calls":[{"index":0,
   "id":"srvtoolu_rl_00_55af2c0e3e4c40079fff41a39c128d7d",
   "type":"deepseek_read_url","kind":"read_link",
   "action":{"type":"read_link","url":"https://example.com"},
   "status":"completed",
   "page":{"url":"https://example.com","title":"Example Domain","published_at":null},
   "signature":"<1904b base64>"}]}]
// message 键序：off = [content, role, server_tool_calls]；on = [content, reasoning_content, role, server_tool_calls]
// read_link 直接读页的 annotations 为 null（url_citation 是 web_search 路径的产物）
```

**③ 多轮回传**：表 t9/t10——回传含 `kind`/`signature` 的 `server_tool_calls` → 第二 turn 零抓取零计费、答案从还原页取；丢弃 → 重复抓取重复计费（官方文档「请求仍成功但页面丢失」实证吻合）。

**④ 计费对账**：8 次计费调用共 4 次计费抓取 ×¥0.005 + ~1.7K in/~0.5K out token ≈ **¥0.027**（实测总花费，远低于 $0.10 预算）；`/user/balance` 精度 ¥0.01 且结算延迟（90s 后才从 51.25 → 51.24），小额度逐单对账在 API 面做不到——**计费数 `tool_fetch` 与真实抓取行为逐调用吻合**（抓=1、未抓=键缺席、signature 还原=0），此为可靠记账依据；平台用量页逐单核对交主会话。

**旁路一次性调用（SSRF 免疫版 webfetch）的可行性随本节实证顺带确认**：read_url 单独声明（thinking off）形态完全可用（t3/t8–t10 的全部证据即其工作形态）。但维持原判记档不排期——它与思考模式自主搜索的卖点无关，且 seek 已有客户端 `webfetch`。

> 原始响应 JSON 留存于执行机 `/tmp/r0-dsx/`（测试卡产物，不进库）；复现命令见卡叙述。

### 2.3 接线设计（**已作废存档**——R0 判红，本节不再实施；保留供 DeepSeek 修复共存性后重启评估时复用）

**pkg/deepseek**：

```go
// types.go —— ChatRequest 加服务端工具开关（与会话稳定的 config 绑定）
type ServerTool struct {
    Type string `json:"type"` // "deepseek_web_search" | "deepseek_read_url"
}
// ChatRequest.ServerTools []ServerTool `json:"-"` —— marshal 时与 Tools 合并输出
// （不改 []Tool：现 Tool.Function 是值类型必序列化，塞 server 工具会带出空 "function"）

// Usage 扩展（计费数与展示数分开，官方口径）：
//   usage.server_tool_use.tool_search / tool_fetch / tool_image_search = 计费数
//   usage.server_tool_use.web_search_requests / web_fetch_requests     = 展示数，不作计费依据
type ServerToolUse struct {
    ToolSearch     int `json:"tool_search,omitempty"`
    ToolFetch      int `json:"tool_fetch,omitempty"`
    ToolImageSearch int `json:"tool_image_search,omitempty"`
    WebSearchRequests int `json:"web_search_requests,omitempty"` // 展示用
    WebFetchRequests  int `json:"web_fetch_requests,omitempty"`  // 展示用
}

// Message 加兄弟字段（镜像 Images 先例，text-only 字节不变保前缀缓存）：
//   ServerToolCalls json.RawMessage `json:"server_tool_calls,omitempty"` // 原样回传（kind/signature）
//   Annotations     json.RawMessage `json:"annotations,omitempty"`       // url_citation，本地展示用
// PrepareForSend 不剥这两个字段（与 PredictedNext 相反——它们必须回传）。
```

**开关设计（默认关，会话稳定）**：

- config 顶层 `server_tools: ["read_url", "web_search"]`（空/缺省 = 关）。**默认关**的理由：①成本形态变化——服务端自主决定何时抓取，单 turn 工具费上限 ~20 次调用 ≈ ¥0.1 + 思考 token（按新价 ¥8/M 输出，50K 思考 token 就是 ¥0.4，**单 turn 成本可放大 ~10×**），默认开等于替用户默许了这笔开销；②tools 数组字节随开关变化，**中途切换会击穿前缀缓存**——所以开关是 config 级、会话启动时定死（与 `/lang` 同纪律，不做 in-session 切换）；
- DeepSeek-only：GLM/二线路径（`pkg/llm`）不认这个字段，接线处按 provider 分叉（照 `ShouldEnableThinking` 的模型判断先例）。

**计费显示**：turn 末从 `usage.server_tool_use` 计费数折算平价（`tool_search`×搜索价 + `tool_fetch`×抓取价 + `tool_image_search`×搜索价），走 `RecordFlatCost`——与 `web_search` 工具的同一条 status bar 累计路径，无需新 UI。注意这是 **turn 级**平价（服务端工具没有本地 tool-call 边界可挂），落点在 `Tracker.Record` 的几个调用站（`update_agent.go:510/702`、print 模式 4 处）旁边加一行 helper 调用，具体形状实现卡定。

**引用展示**：`annotations.url_citation` 在 TUI 里淡显为脚注列表（照 `[image: ` 标记族的 replay 样式规则扩展，wire-format 纪律：新前缀 `"[cite: "` 钉 `HasPrefix` 测试）。

### 2.4 里程碑（2h 卡粒度）——**R0 判红，R1–R4 全部关闭**

| 卡 | 内容 | 依赖 | 验收 |
|---|---|---|---|
| **R0 验证门** | curl 真 API（`.env` key）：①function tool + read_url 同请求是否 200；②thinking on/off 两形态的响应/usage 字段名实证（chat/completions 协议下 `server_tool_use` 的确切 JSON 形状）；③两轮对话验证 server_tool_calls/signature 回传与丢弃的差异（第二 turn 是否重复抓取）；④抓取计费数与账单对得上 | — | ✅ **已执行 2026-11-14，判红（假绿）**——四点结论与字段实证见 §2.2；①降级为不接，R1–R4 关闭 |
| ~~R1 `pkg/deepseek` 基座~~ | ~~`ServerTool` 类型 + 合并 marshal；`Usage.ServerToolUse` 解析；兄弟字段 round-trip~~ | ~~R0~~ | **关闭（R0 红）** |
| ~~R2 agent 接线 + 开关~~ | ~~config `server_tools` 键；agent 注入；`RecordFlatCost` 折算~~ | ~~R1~~ | **关闭（R0 红）** |
| ~~R3 多轮回传 + TUI~~ | ~~server_tool_calls 存 session 原样回传；annotations 淡显~~ | ~~R2~~ | **关闭（R0 红）**（多轮回传语义本身已被 §2.2 t9/t10 实证，供未来重启复用） |
| ~~R4 文档~~ | ~~`guide-config.md` 增段~~ | ~~R3~~ | **关闭（R0 红）** |

---

## 3. ② 文件输入 PDF/Office —— **接**

### 3.1 为什么接

agent 读 PDF/Word/PPT 是高频真需求（论文、合同、报告、幻灯片）。现状绕行路径 = bash + `pdftotext`/`pandoc` 这类**不一定安装**的 CLI，且丢版面。文件解析接口把这些整个变成服务端能力：上传即解析、按页计费、图文可选。管道与 [feature-vision](feature-vision.md) 的 imgrefs 先例**逐点同构**，没有新发明：

| 环节 | imgrefs 先例 | filerefs 对应 |
|---|---|---|
| 引用检测 | `imgrefs.Detect`（stat-gated 防假阳性 + `cleanToken` 剥壳） | `filerefs.Detect`：扩 `.pdf .doc .docx .ppt .pptx` 后缀表，同 stat 门（代码片段里的 ".pdf" 子串不是文件） |
| 提交期路由 | `visionRouter`（TUI/print/ACP 三入口共用，永不 error） | `fileRouter` 同构：`[file: <name> — …]` in-band 标记族（新前缀钉测试） |
| 持久层 | copy-at-submit 到 `~/.seek/projects/<id>/assets/`（sha256 内容寻址） | **同库复用**——原始文档是唯一真相源（服务端**不支持下载**原文件，本地资产库是唯一副本） |
| 发送层 | `ResolveImages`：Asset → data URL | `ResolveFiles`：FileID 缓存命中直接引用；**过期/缺失 → 从资产库重上传再引用**（决策完全可逆，镜像 vision §7.2 的兜底论证） |
| 会话形态 | `Message.Images` 兄弟字段 + 双形态序列化 | `Message.Files` 兄弟字段；wire 形态在 content 数组加 `{"type":"file","file_id":…}` 分块 |

### 3.2 设计决策

**D-F1 purpose 路由：默认 `text_recognition`，rich 是显式升级。** 文字版上下文更省（图表图占 token）、解析费同价；`rich_layout_recognition` 只在用户点名要图（「看下这页的图表」）时用。切换面 v1 = 前缀语法 `@rich:report.pdf`（同消息可混用），不进 config——这是 per-file 属性不是全局偏好。

**D-F2 `user_data` 类不接。** txt/md/csv/json/代码 seek 已有 `read` 工具（免费、无上传往返）；XLSX/EPUB 属低频，v1 后缀表只含 PDF/Word/PPT 六个扩展。后缀表是数组常量，将来加一行即扩。

**D-F3 file_id 是传输优化，不是状态。** 资产库存原文件（唯一副本），`~/.seek/projects/<id>/filerefs.json` 记 `sha256 → {file_id, purpose, uploaded_at, expires_at}` 的映射缓存（**这是传输缓存不是平行状态**——丢了可重建，镜像 plan artifact 的 write-once 定位而非 `reconstruct` 纪律之争；实现卡里如发现可完全无状态化——每次会话首轮懒上传——则砍掉此文件，**先试无状态版**）。

**D-F4 大小/页数门在路由器。** 100 MiB / 1000 页 / 每请求 20 文档 / 8 MiB 文字合计——超限 in-band note 降级（照 vision 的 never-error 哲学），不 fatal。

**D-F5 web 面拖拽 = 契约注记，不是本卡交付。** Web 面板（[feature-inspect-rpc](feature-inspect-rpc.md)，📐 未交付）与 control-plane web frontend 都在未排期状态。这里只立契约：未来 `Inbound`（control-plane D-C1）加可选 `Attachments []Attachment{Path,Mime}` 字段，平面原样落盘后走与 TUI 相同的 `fileRouter`（拖拽 = 平面写临时文件 + 注入 `@路径` 引用，**不新造管道**）。验收：契约字段写进本文即算（不写代码）。

**D-F6 前缀缓存。** `file_id` 字符串逐轮字节稳定 → 与 base64 图同理由可参与前缀缓存（vision M-V.3 已实测 base64 图参与缓存；file_id 是否等价**未验证**——照 vision §7.2 的先例，实测归入 F4 验收，结论无论哪边记 `docs/pitfalls.md`）。

### 3.3 里程碑（2h 卡粒度）

| 卡 | 内容 | 验收 |
|---|---|---|
| **F1 `pkg/deepseek` files 客户端** | `UploadFile`（multipart，purpose 在 file 前）、`FileObject` 类型、wire 形态 `file` 分块进 `ChatRequest.MarshalJSON`（与 `image_url` 分块同站） | httptest fake 断言 multipart 字段序 + wire 形状；text-only 字节等价 guard |
| **F2 `internal/filerefs` 检测器** | 后缀表 + stat 门 + `cleanToken` 复用（从 imgrefs 提或直接内联，倾向复制保持两包独立——检测语义已分叉） | 假阳性用例照 imgrefs 测试集（代码片段/URL 里的 `.pdf` 不是引用） |
| **F3 提交期路由 + copy-at-submit** | `fileRouter`（三入口共用）；资产库写入；上传 + 解析费 `RecordFlatCost`（按返回页数）；`@rich:` 前缀解析；in-band 标记族 | 提交 `@report.pdf` → 标记 + file_id 就位；100 MiB 超限降级不 fatal |
| **F4 发送路径 + resume + 缓存实验** | `ResolveFiles`（懒上传/过期重传）；session round-trip；侧信道 `WithoutFiles`（照 `WithoutImages`）；**file_id 前缀缓存实测**（同文档两轮，看 `prompt_cache_hit_tokens`） | 真 API：上传→引用→模型答出文档内容；resume 后继续；实验结论记 pitfalls |
| **F5 展示 + 文档 + 契约注记** | replay 淡显标记（`[file: ` 族）；`guide-vision.md` 或新 `guide-files.md`；`Inbound.Attachments` 契约段落（D-F5）；价格表核对（§六） | 文档链路齐 |

---

## 4. ③ OCR / ASR / TTS —— OCR 不接；ASR+TTS 接客户端层，语音 frontend 分期

### 4.1 OCR：不接

OCR 独立接口（`/v1/tools/ocr`）与文件解析同价（¥1.5/千页）但**产物不进对话**（「文件不会存入 Files API，也不会交给对话模型」）——它输出的是带坐标标记的纯文本。seek 的消费场景是「模型读文档作答」，那是②的领地；「坐标级版面提取」（批量表格数字化之类）没有 seek 侧消费者，且 TTS 那句 21 语种/逐页坐标的管线对 CLI agent 是负担不是能力。**不接；若未来出现坐标消费场景（如 UI 自动化的元素定位），再单开评估卡。**

### 4.2 ASR + TTS：客户端层接（便宜、零风险），frontend 分期

客户端两张卡照 `search.go` 抄（零依赖、同 key 同 base URL、`retryCall` 同款）：`pkg/deepseek/tts.go`（HTTP 整段，text→wav/pcm；字符计费数本地可算——汉字×2）、`pkg/deepseek/asr.go`（multipart，WAV/Opus ≤8 MiB ≤120s，返回 `text`+`duration_ms`）。**它们是 control-plane 语音 frontend 的原料**（卡面原话），先落客户端是因为：不依赖 frontend 选型、单价便宜到无所谓、且把 API 形状（尤其计数字段）钉进测试。

### 4.3 语音渠道骨架（control-plane 语音 frontend 设计稿）

宿主：[feature-remote-control-plane](feature-remote-control-plane.md) 的 `Frontend` 端口（`Run` 收 / `Send` 发 / `Name`，收发分离、不假设双工——§1.3 的设计前提正好为语音定制）。M4/M5（飞书）已撤销排期，**首个 frontend 选型开放**——语音可以是其中之一。

```
┌─ VoiceFrontend（实现 Frontend 端口）─────────────────────────┐
│ Run()  入站：录音（push-to-talk，≤120s WAV）→ ASR → Inbound.Text │
│        （v1 不实现：Run 直接阻塞返回 nil —— 纯出站形态）          │
│ Send() 出站：Reply.Text → TTS（stella）→ 本地/远端音频播放        │
│ ConvID = "voice:<device-id>"                                    │
└──────────────────────────────────────────────────────────────┘
```

**分期结论：v1 只做出站 TTS 播报，v2 加 push-to-talk 入站，不做实时双工。**

- **v1 出站播报**（约 2 张 2h 卡）：`Send` = TTS 合成 + 播放。消费面两个：①serve 平面的 `Reply` 出口（远程会话的回复合成播报到指定设备——与 mobile-push 同一句「离开电脑也能收」）；②routines/cron 通知面（任务完成播报，走 `Notifier` 的可选 sibling 接口，照 WebhookDispatcher 先例不破主契约）。出站先行是因为收发不对称下**单向是完整可用的产品**（播报通知），而入站没有配套 TUI/交互设计撑不起来。
- **v2 push-to-talk 入站**（约 3 张 2h 卡）：录音边界由客户端按键定（ASR 非流式、120s 上限——语音条形态天然契合，录完再传）；`DirectAt` 语义 = 按下即 true（无「群里 @」对应物）；`EventID` = 录音会话 id 去重。多轮体验 = 语音条进 → 文字回复（TUI/推送）或 TTS 播报（全语音设备）。
- **明确不做：实时双工流式对话。** 硬约束摆在那：ASR **无流式**（文档原话）、TTS **不支持中断续传**、无语速调节。等 DeepSeek 出流式 ASR 再评估，不拿轮询假造。
- 计费量级：播报一条 100 字通知 ≈ 200 计费字符 × ¥3/万 = **¥0.06**（5 折期 ¥0.03）；一条 30s 语音条 ≈ ¥0.0065（5 折）。单条成本可忽略，但 serve 是常驻平面——**用量护栏走平面层节流**（每会话每分钟播报上限），不靠价格自觉。

**里程碑（2h 卡粒度）**：

| 卡 | 内容 | 依赖 | 验收 |
|---|---|---|---|
| S1 TTS 客户端 | `pkg/deepseek/tts.go` + `pricing.TTSCost`（按计费字符，汉字×2） | — | httptest fake + 真 API smoke（stella 合成 100 字） |
| S2 ASR 客户端 | `pkg/deepseek/asr.go` + `pricing.ASRCost`（按秒向上取整） | — | 同上（WAV 往返转写） |
| V1a 播报 sink | serve 侧 `Speaker`：Reply → TTS → 播放（macOS `afplay`/Linux `aplay|paplay`/降级写 wav 文件）；routines `Notifier` sibling 接口 | S1 | serve 会话回复合成播放；节流上限生效 |
| V1b 语音条格式与队列 | 播报队列（串行、可静音开关 `voice.enabled`）；长文截断（10K 计费字符上限） | V1a | 连续回复不叠音 |
| V2 入站 push-to-talk | VoiceFrontend.Run 完整实现（录音→ASR→Inbound）；`Inbound.DirectAt` 语音语义 | S2 + 首个 serve frontend 落地 | 手机/网页按住说话 → seek 收到文字并回复 |

（V2 依赖「首个 serve frontend 选型」落地——语音入站需要一个录音 UI 载体，纯终端按键录音体验差；此依赖写明，不虚排。）

---

## 5. ④ 模型安全校验 `safety_check` —— **接**（小卡）

- **默认关**（自用）：开启后模型输入输出过审，命中风险返回 `refusal`——对 seek 的 agent 场景（含安全研究、漏洞分析类合法工作）是纯能力损失 + 延迟，且 seek 无对外服务义务。
- **暴露为 config 可选项**：顶层 `safety_check: "high"`（缺省/空 = 关；只认 `"high"` 一个档，文档只给了这一档，枚举留扩展）。会话启动读一次，与 `server_tools` 同纪律不做 in-session 切换。
- **refusal 面**：`finish_reason: "refusal"` 目前 seek 无处理（已核实无任何 refusal/content_filter 处理点）——SC1 里加：该 finish_reason → assistant 消息记 in-band 标记 `[safety: 拒答（safety_check 命中）]`，turn 正常结束不 fatal（照 never-error 哲学）；`content_filter` 同路处理。
- 合规注记进 `guide-config.md`：若用 seek 对外提供服务，开启本参数是用户的法定义务侧事项，seek 只提供开关。

**里程碑**：**SC1**（2h）——config 键 + `ChatRequest.SafetyCheck` 字段 + agent 接线（DeepSeek 车道 only）+ refusal 处理 + httptest fake 测试（refusal 响应路径）+ guide 行。一张卡完事。

---

## 6. 价格表更新点（列全）

`internal/pricing` 现有：token 三口价表 + `searchPerCallUSD`（¥5/千次 ≈ $0.000714/call）。本次发布面新增/变更点：

| # | 条目 | 值（刊例） | 落点 | 卡 |
|---|---|---|---|---|
| 1 | 网页抓取（fetch） | **¥5/千次**（$0.75/1K ≈ $0.00075/call，7.0 汇率同 GLM 行口径） | `fetchPerCallUSD` 常量 + `FetchCost(n)`；消费点 = read_url 的 `usage.server_tool_use.tool_fetch` 折算（含被 safety 替换页——官方口径照抄进注释） | R2 |
| 2 | 服务端搜索计费数折算 | 搜索价复用 `SearchCost`；**计费数取 `tool_search`/`tool_image_search`，不取展示数 `web_search_requests`** | turn 末平价 helper（§2.3） | R2 |
| 3 | 文件解析 | **¥1.5/千页**（`text_recognition`/`rich_layout_recognition` 同价，官方未分档） | `parsePerPageUSD` + `ParseCost(pages)`；消费点 = F3 上传成功后按返回页数 `RecordFlatCost` | F3 |
| 4 | TTS | **¥3/万计费字符**（汉字×2；限时 5 折 ¥1.5，**无截止日期**——常量用刊例价，折扣进注释，与 search 的处理一致：宁可高估不低估） | `TTSCost(chars)` | S1 |
| 5 | ASR | **¥0.78/小时**，按秒向上取整（5 折 ¥0.39 同上处理） | `ASRCost(seconds)`（内部 ceil） | S2 |
| 6 | OCR | ¥1.5/千页——**不接不进表**，注释里留一行「OCR 同价未接，见 feature-deepseek-app-api §4.1」 | — | — |
| 7 | ✅ **已收口（2026-11-15 联网核）** | en 页 USD peak 与 pricing.go 逐位一致（flash 未过期）；「调价」系 zh/en 两页口径分叉的跨币种误判；v4-pro 独立行为本卡真实增量 | 已随本卡收口：chapter-13 §13.3 指针行已补、commands.go 过时注释已正、pricing.go 注释改锚 en 页 | 完成 |

**叠加口径（写进 pricing.go 注释的规范表述）**：单请求总费用 = `Cost(model, tier, usage)`（token，峰谷折价）+ Σ工具平价（per-call/per-page/per-char/per-second，**不折峰谷**）。工具平价全部走 `RecordFlatCost` 家族进 `CumulativeCost`，status bar 无需新 UI。

**serve 侧记账缺口**（设计注记）：TTS/ASR 发生在 control-plane serve 进程里，那里**没有 session Tracker**——v1 播报记 serve 本地日志计数（`~/.seek/serve/` 下 append JSONL 即可，与 bindings 同目录纪律），不强行接进会话成本；等 serve 有了自己的用量面板再统一。

---

## 7. 测试计划（对照 AGENTS.md failure-mode 标准）

| 面 | 用例 |
|---|---|
| 序列化（R1/F1） | text-only **字节等价 guard**（前缀缓存双保险）；server 工具/file 分块的 wire 形状断言；session round-trip（ServerToolCalls/Annotations/Files 兄弟字段） |
| 畸形输入 | API 400（purpose 字段序错、user_data 传 PDF、超 1000 页、ASR 超 120s/8 MiB、TTS 超 10K 字符）；`finish_reason:length` OCR 页（不接，只注释）；refusal 响应路径（SC1） |
| 降级 | 资产库文件缺失 → in-band note 不 fatal（F4）；file_id 过期 → 重上传自愈（F4）；100 MiB 超限（F3）；播放器缺失 → 写 wav 文件降级（V1a） |
| 中断/恢复 | 提交后上传前取消（资产已入库、file_id 未建——resume 幂等）；TTS 流中途断开视为失败（重试一次） |
| 计费 | `tool_fetch` 含 safety 替换页的折算；汉字×2 的 TTS 计费数；ASR 秒级 ceil；**真 API 账单对数**（R0/F4/S1 smoke 顺带） |
| 并发 | serve 播报队列串行（V1b）；15 并发上传限流的面（429 重试一次，照 `retryCall`） |
| e2e | 真 API smoke 门控（`.env`，照 vision/websearch 惯例）：read_url 两轮不重复抓取；PDF 引用后模型答出内容；TTS/ASR 往返 |

---

## 8. 决策记录与开放问题

1. **read_url 判红不接**（2026-11-13 缓 → **2026-11-14 R0 实测红**）：共存性实测为假绿——thinking off 混用 400、thinking on 混用 200 但 server tool 被静默剥除（模型工具清单里没有它），两形态交集为空，agent 主路径不可用。web_search 变体同规则 400。R1–R4 关闭；旁路一次性调用（read_url 单独声明形态已实证可用，§2.2 t3/t8–t10）维持记档不排期。重启条件：官方修复 function tools 与 server tools 混用（届时 §2.3 存档设计可复用）。
2. **文件输入默认 text_recognition**：图表场景用 `@rich:` 前缀 per-file 升级，不进 config（per-file 属性非全局偏好）。
3. **语音 v1 只出站**：`Frontend` 端口收发分离的设计前提 + ASR 非流式/TTS 无中断的硬约束，单向播报是唯一「完整可用且不假造能力」的 v1 形态。实时双工明确不做，等官方流式 ASR。
4. **safety_check 默认关**：自用无对外义务，拒答是纯能力损失；config 暴露满足「将来对外开」的一天。
5. **工具平价不折峰谷**：官方工具价无峰谷之分，与 token 费分开口径（§1.5）。
6. **开放问题**：①~~read_url 与 function tools 共存性~~（**已解决 2026-11-14，R0 判红**：off 混用 400 / on 混用静默剥除，见 §2.2）；②`file_id` 前缀缓存参与性（F4 实验）；③~~chat/completions 协议下 `server_tool_use` 确切字段名~~（**已实证 2026-11-14**：计费数 `tool_search`/`tool_fetch`/`tool_image_search`，全零时整键缺席；展示数未见出现，§2.2）；④serve 侧用量面板（远期）；⑤filerefs 映射缓存 vs 无状态懒上传（F4 定，倾向先试无状态）。

---

**与路线图的关系**：①②是 DeepSeek-first 深化（`pkg/deepseek` 一等公民的下一批字段）；③是 control-plane（v0.11 平台化线）语音 frontend 的原料前置；④是合规底座补完。全部不碰 `pkg/deepseek` 零外部依赖铁律（TTS/ASR/files 客户端照 `search.go` 的纯 stdlib 形态）。
