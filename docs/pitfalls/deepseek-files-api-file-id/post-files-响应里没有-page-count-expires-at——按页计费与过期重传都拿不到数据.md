### POST /files 响应里没有 page_count / expires_at——按页计费与过期重传都拿不到数据
- **Saw**: 上传只回 `{id, object, bytes, created_at, filename, purpose}`，`page_count`/`expires_at` 不回；`pricing.ParseCost(fo.PageCount)` 恒 $0、`expires_at=0` 重传不触发。
- **Why**: 响应是 `FileObject` 最小子集（`text_recognition`/`rich_layout_recognition` 均缺）；`GET /files/{id}`/`GET /files` 同表 `id/object/bytes/created_at/filename/purpose[+expires_at]`。
- **Fix**: `0 = 未声明过期 = 不主动重传`；`files_smoke_test.go` 记缺失为读数；缺页数记 `pricing.EstimateParsePages(bytes)`（卡 c_y2akdi，`ceil(bytes÷104857)`，1000 页上限，旗标 `~N pages·估`+`FilePart.PagesEstimated`）
- **Lesson**: 文档字段 ≠ 实际响应字段，算钱字段必须真机核对；页数拿不到按官方上限估算打旗。
- **Refs**: `pkg/deepseek/files.go`、`internal/fileroute/fileroute.go`、`docs/prd/archive/feature-deepseek-app-api.md` §1.2/§6