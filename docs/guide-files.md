# Files — 文档输入（PDF / Word / PPT）

seek 支持把 PDF / Word / PPT 文档直接发给 DeepSeek：提交时上传并解析，模型按解析后的文字（或图文混排）作答——不用本地装 `pdftotext` / `pandoc` 这类不一定存在的 CLI，版面理解在服务端完成。

## 用法

- **引用**：输入里写已存在的文档路径，`@report.pdf` 或裸路径均可（stat-gated：只有真实存在的文件会被识别，代码片段 / URL 里的 `.pdf` 子串不算）。后缀表：`.pdf .doc .docx .ppt .pptx`；同一消息可引用多个文档。
- **图文混排**：默认按纯文字解析（`text_recognition`——图表丢弃，上下文最省）；点名要看图表时写 `@rich:report.pdf`（`rich_layout_recognition`——图占输入 token，解析费同价）。同一消息可混用两种形态；同一文件先写哪种就按哪种解析。
- **车道**：DeepSeek 车道（`--provider=deepseek`）走 Files API，提交时上传解析，PDF / Word / PPT 都收。二线车道（Anthropic / OpenAI / Gemini / GLM / OpenCode Go / OpenDesign）按模型能力（`internal/modelcap.Files`）走「存本地 + 发送时内联」：**只有 PDF** 能原生透传（三家官方文档的文档输入都只收 PDF），Word/PPT 在该车道提交期即降级为 `[file: … 二线车道仅支持 PDF 附件]` 提示，不入库、不上传、不收费。模型不支持文件输入时是 `[file: … 当前模型不支持文件输入，用 /model 选一个支持文件的模型]`。

## 行为细节

- **资产库**：提交时文档被复制到 `~/.seek/projects/<id>/assets/`（sha256 内容寻址，相同内容自动去重）。服务端**不支持下载**原文件与解析结果，本地这份是唯一副本——file_id 过期后发送路径会从它按原 purpose 自动重传，无需重新提交。
- **计费**：解析按页一次性计费 ¥1.5/千页（$0.225/1K），上传成功才收；对话引用不再收解析费（按拼入上下文的输入 token 计）。上传响应不含页数（实测见 `docs/pitfalls.md` "DeepSeek Files API"），所以缺页数时 seek 按文件字节数**估算**记费（`ceil(bytes÷104857)`，仅为「不留 $0」的兜底，误差方向随格式漂），并在标记与 transcript 里打「估」旗（`~N pages·估` / `pages_est`）；要精确数请以平台账单页为准。
- **前缀缓存**：参与，且比图片更稳——文档按「解析后的文字」进 prompt，file_id 本身不占 token，换 id / 过期重传都**不击穿缓存**（实测同文档两轮 91% 命中）。
- **限制**：单文档 ≤ 100 MiB；每请求 ≤ 20 个文档、文字合计 ≤ 8 MiB；每账号同时 15 个文档；file_id 有效期 1h–30 天或永久。超限 / 失败一律降级为 in-band 提示，不 fatal。
- **回放**：文档消息渲染为折叠标记 `[file: 名字 — uploaded · text · 12 pages · file-api-xxxx]`（DeepSeek 车道）或 `[file: 名字 — attached · 12 KiB]`（二线车道；暗色，与图片标记同一样式；估算页数写作 `~12 pages·估`）；读取失败 / 超限 / 上传失败 / 格式不支持 / 模型不支持也是同族标记，原文路径保留在 transcript 里。
- **纯文本类不走上传**：txt / md / csv / json / 代码由 `read` 工具免费直读（无上传往返）；XLSX / EPUB 暂不在后缀表。

## 二线 provider（Anthropic / OpenAI / Gemini 等）

**PDF 透传**（2026-11-19，按图片先例）。三家的文档输入 wire 格式各不相同、交集是 PDF-only，因此：

- OpenAI 兼容车道（含 GLM、OpenCode Go、OpenDesign Cloud）：数组形 `content` 里一个 `{"type":"file","file":{"filename":…,"file_data":"data:application/pdf;base64,…"}}` 块；纯文本消息仍是纯字符串，字节与改动前逐字节一致。
- Anthropic：`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":…}}` block。
- Gemini：`{"inlineData":{"mimeType":"application/pdf","data":…}}` part。

能力判定是 `internal/modelcap.Files(model)`（Vision 的姊妹维，同一套静态表 + 目录 grant；目录里 `modalities.input` 声明 `application/pdf` 才 grant）。**格式**判定是另一轴：`pkg/llm` 的 `fileMediaTypes`（今天只有 `application/pdf`），由 `File.Sendable()` 在转换时把关——所以能力表说「这个模型收文档」不承诺「这个车道收这个格式」。

> **OpenDesign 车道的文档有个已知洞**：`deepseek-v4.1-flash` 在能力表里是 `true`（OpenCode 车道真支持），但 OpenDesign 网关会**静默丢弃** `file` part —— 回 200、模型答「没有附件」。要在这个车道传文档，用 `claude-haiku-5.5` / `gpt-6-luna` / `glm-5.3-flashx`。根因与取舍见 [`docs/pitfalls/llm-provider-quirks-m6/`](pitfalls/llm-provider-quirks-m6/)。

不支持的车道/格式一律 in-band 降级：模型不支持 → `[file: N 个文档未透传 — 当前模型不支持文件输入…]`；格式不支持 → 同一条提示补「仅支持 PDF」。**不会**拿一个 400 毁掉整个回合。发送时不占解析费（没有 Files API），文档字节按输入 token 计费，与图片同理。
