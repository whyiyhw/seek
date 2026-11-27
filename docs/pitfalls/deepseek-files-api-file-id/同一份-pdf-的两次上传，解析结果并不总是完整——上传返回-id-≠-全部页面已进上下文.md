### 同一份 PDF 的两次上传，解析结果并不总是完整——上传返回 id ≠ 全部页面已进上下文
- **Saw**: 烟测（2026-11-18）同一 2 页 PDF：多数 `prompt_tokens≈1941`，一次 `1079`——第 2 页口令答 `NOT IN DOCUMENT`，再发（`1102`）仍缺。
- **Why**: 未定论——或某页未就绪，或截断；反例见过（`prompt_tokens≈1022` 仍答对）。
- **Fix**: 烟测只断言第 1 页事实（`files_smoke_test.go`）；`GET /files/{id}`/`GET /files` 只有 `id/object/bytes/created_at/filename/purpose`（`expires_at` 仅设过才现）、无 `page_count`（卡 c_y2akdi；`POST /v1/tools/ocr` 不入 Files API）；transcript 记 `{file_id, 声称页数, 估旗}`（`FilePart.Pages` / `PagesEstimated`）对账。
- **Lesson**: e2e 断言挑最不易被实现细节削掉的证据——同页内的事实比「某页存在」稳。
- **Refs**: `pkg/deepseek/files_smoke_test.go`；上一条 `page_count / expires_at`。
