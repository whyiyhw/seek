### 卡面照单落地前先核前提——PRD 里点名要复用的子系统可能在卡写下之后已整体下线
- **Saw**: 卡 c_4pczu3（ACP·P1-3）要求「图片块 base64→临时 PNG→复用 `ocr.Run`（柱 Q Vision）→`[image: … — OCR]`」，但仓库没有 `internal/ocr` / `ocr.Run` / `ocr.RunBytes`，`go build` 失败。
- **Why**: 第 ③ 步抄自 `docs/prd/archive/feature-acp.md` §8（M-P.5 细化稿），而柱 Q OCR 引擎已在 `0ecfd6f6`（feature-vision M-V.0）整体下线，「OCR 收敛为降级路径」方案同日被推翻（`feature-vision.md`）；归档 PRD 不再更新，步骤静默指向已删符号。
- **Fix**: 改走 PRD 记的继任者 `visionRouter`（`cmd/seek/visioninput.go`，TUI 粘贴与 `@引用` 共用）：视觉模型原生附图、非视觉模型行内注明；三条验收意图（块收下/按序拼接/只注入文字）保留，只换来源；偏离理由写回 PRD §8 与 M-P.5 行。
- **Lesson**: 卡面写死的实现路径只是当时的正确路径——动手先 grep 符号在不在，不在就找 PRD 记的继任者，偏离写回 PRD。
- **Refs**: `docs/prd/archive/feature-acp.md` §8, `docs/prd/archive/feature-vision.md`, `cmd/seek/visioninput.go`, commit `0ecfd6f6`.
