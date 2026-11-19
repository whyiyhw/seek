# Files — 文档输入（PDF / Word / PPT）

seek 支持把 PDF / Word / PPT 文档直接发给 DeepSeek：提交时上传并解析，模型按解析后的文字（或图文混排）作答——不用本地装 `pdftotext` / `pandoc` 这类不一定存在的 CLI，版面理解在服务端完成。

## 用法

- **引用**：输入里写已存在的文档路径，`@report.pdf` 或裸路径均可（stat-gated：只有真实存在的文件会被识别，代码片段 / URL 里的 `.pdf` 子串不算）。后缀表：`.pdf .doc .docx .ppt .pptx`；同一消息可引用多个文档。
- **图文混排**：默认按纯文字解析（`text_recognition`——图表丢弃，上下文最省）；点名要看图表时写 `@rich:report.pdf`（`rich_layout_recognition`——图占输入 token，解析费同价）。同一消息可混用两种形态；同一文件先写哪种就按哪种解析。
- **车道**：文件输入走 DeepSeek Files API，仅 `--provider=deepseek` 车道可用；其他车道（glm / opencode / 二线 provider）提交时降级为 in-band 提示，不上传。

## 行为细节

- **资产库**：提交时文档被复制到 `~/.seek/projects/<id>/assets/`（sha256 内容寻址，相同内容自动去重）。服务端**不支持下载**原文件与解析结果，本地这份是唯一副本——file_id 过期后发送路径会从它按原 purpose 自动重传，无需重新提交。
- **计费**：解析按页一次性计费 ¥1.5/千页（$0.225/1K），上传成功才收；对话引用不再收解析费（按拼入上下文的输入 token 计）。注意：上传响应不含页数（实测见 `docs/pitfalls.md` "DeepSeek Files API"），seek 的成本条暂时记不出这笔解析费——以平台账单页为准。
- **前缀缓存**：参与，且比图片更稳——文档按「解析后的文字」进 prompt，file_id 本身不占 token，换 id / 过期重传都**不击穿缓存**（实测同文档两轮 91% 命中）。
- **限制**：单文档 ≤ 100 MiB；每请求 ≤ 20 个文档、文字合计 ≤ 8 MiB；每账号同时 15 个文档；file_id 有效期 1h–30 天或永久。超限 / 失败一律降级为 in-band 提示，不 fatal。
- **回放**：文档消息渲染为折叠标记 `[file: 名字 — uploaded · text · 12 pages · file-api-xxxx]`（暗色，与图片标记同一样式）；读取失败 / 超限 / 上传失败也是同族标记，原文路径保留在 transcript 里。
- **纯文本类不走上传**：txt / md / csv / json / 代码由 `read` 工具免费直读（无上传往返）；XLSX / EPUB 暂不在后缀表。

## 二线 provider（Anthropic / OpenAI / Gemini 等）

不透传文档：带文档的历史消息会附一条「N 个文档未透传」提示，模型看得到标记文字、看不到文档内容。
