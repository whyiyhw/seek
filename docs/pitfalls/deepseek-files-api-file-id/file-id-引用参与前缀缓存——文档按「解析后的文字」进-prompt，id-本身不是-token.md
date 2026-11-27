### file_id 引用参与前缀缓存——文档按「解析后的文字」进 prompt，id 本身不是 token
- **Saw**: F4 真机实测（2026-11-18，卡 c_5gy5h6）：同文档两轮，第 2 轮 `prompt_tokens=1964 / hit=1792 / miss=172`；对照臂 `1205→1220`、`hit=1024`；resume 再发 `1962 / 1792 / 170`；换新 file_id（同字节）首轮仍 `hit=1792`。
- **Why**: `{"type":"file","file_id":…}` 不进 token 流——服务端先展开解析文字再拼 prompt，缓存按展开后序列命中；换 id/重传不击穿缓存，只有内容或前序消息变化才 miss。
- **Fix**: 无代码改动；实验固化在 `pkg/deepseek/files_smoke_test.go` `TestFiles_RealAPI_UploadReferenceResumeCache`。
- **Lesson**: 引用形态是否参与缓存要实测（base64 图 M-V.3 亦然）：file_id 可缓存且比图 URL 稳——id 变了照样命中，重传不多付 cache miss。
- **Refs**: `docs/prd/archive/feature-deepseek-app-api.md` §3.2 D-F6、§5 开放问题②；`pkg/deepseek/resolve.go`；`docs/prd/archive/feature-vision.md` M-V.3。
