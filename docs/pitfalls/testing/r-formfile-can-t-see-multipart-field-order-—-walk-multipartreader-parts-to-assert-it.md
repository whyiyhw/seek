### `r.FormFile` can't see multipart FIELD ORDER — walk `MultipartReader` parts to assert it
- **Saw**: F1 needed a test proving the Files API upload body puts the `purpose` field part BEFORE the `file` part (the API 400s on misorder, PRD feature-deepseek-app-api §1.2), but `r.FormFile`/`r.FormValue` answer only "which value", never "which position".
- **Why**: `ParseMultipartForm` (which `FormFile`/`FormValue` call under the hood) stores parts in `Value`/`File` maps — ordering is dropped at parse time. Only the sequential `multipart.Reader.NextPart()` stream preserves wire order.
- **Fix**: `readUploadParts` in files_test.go walks `r.MultipartReader()`'s `NextPart` sequence and asserts position 0 = `purpose` (plain field, no filename), position 1 = `file`. Commit (this one).
- **Lesson**: when the CONTRACT is part order (some APIs — OpenAI-style Files endpoints especially — reject misordered multipart fields), assert on the raw part stream, never on the parsed form. The parsed form is fine for presence/content, blind to order.
- **Refs**: `pkg/deepseek/files_test.go:readUploadParts`, `pkg/deepseek/files.go:doUploadFile`
