### 卡面照单落地前先核前提——PRD 里点名要复用的子系统可能在卡写下之后已整体下线

- **Saw**: 卡 c_4pczu3（ACP·P1-3）的验收原文要求「图片块 base64→临时 PNG→复用 `ocr.Run`（柱 Q Vision）→`[image: … — OCR]`」，但仓库里既没有 `internal/ocr` 包，也没有 `ocr.Run` / `ocr.RunBytes`——`go build` 直接失败，改不动。
- **Why**: 那张卡的第 ③ 步抄自 `docs/prd/archive/feature-acp.md` §8（M-P.5 细化稿），而柱 Q 的 OCR 引擎已在 `0ecfd6f6`（feature-vision M-V.0）**整体下线**，`feature-vision.md` 还明确记录了「OCR 收敛为非视觉模型的降级路径」这一方案同日被推翻。PRD 归档意味着"不再更新"，于是它的实现步骤会静默指向已删除的符号。
- **Fix**: 走同一份 PRD 记下的**继任者**——`visionRouter`（`cmd/seek/visioninput.go`，TUI 粘贴与 `@引用` 共用的那条缝）：视觉模型原生附图、非视觉模型行内注明。三条验收意图（块收下 / 按序拼接 / 只注入文字不送字节给吃不下图的模型）全部保留，只换了"文字从哪来"。偏离理由写回 PRD §8 与 M-P.5 行，而不是悄悄改代码（c_4pczu3）。
- **Lesson**: 卡面写死的实现路径是**当时**的正确路径。动手第一步是 `grep` 那个符号还在不在（一次 grep 比一次编译失败便宜），不在就找 PRD 里记的继任者，并把「为什么偏离原文」写回 PRD——否则下一个人读到的仍是一份指向死代码的步骤表。
- **Refs**: `docs/prd/archive/feature-acp.md` §8, `docs/prd/archive/feature-vision.md`（M-V.0 推翻记录）, `cmd/seek/visioninput.go`, commit `0ecfd6f6`.
