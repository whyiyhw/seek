### file_id 引用参与前缀缓存——文档按「解析后的文字」进 prompt，id 本身不是 token
- **Saw**: F4 真 API 实测（2026-11-18，卡 c_5gy5h6，2 页 PDF ≈12741 字节）：同文档两轮，第 2 轮 `prompt_tokens=1964 / hit=1792 / miss=172`（91% 命中）；加长后的无文档对照臂 `1205→1220`、`hit=1024`，文件臂比对照臂多出的 ≈736 token **全部落在命中侧**。session JSON 往返（resume）后再发一轮：`1962 / 1792 / 170`——字节稳定到缓存连得上。另一次运行换了一个全新的 file_id（文档字节相同，走的是 ResolveFiles 的懒上传路径），首轮同样 `hit=1792`。
- **Why**: `{"type":"file","file_id":…}` 不是文本——file_id 串不进 prompt token 流；服务端在拼 prompt 前把解析结果展开成文档文字，缓存按展开后的 token 序列命中。所以换 id、重新上传都不击穿前缀缓存，只有文档内容或前序消息变化才会。
- **Fix**: 无代码改动（结论本身是交付物）；实验形状固化在 `pkg/deepseek/files_smoke_test.go` `TestFiles_RealAPI_UploadReferenceResumeCache`（文件臂 + 文本对照臂 + resume 臂，断言命中覆盖文档权重的一半以上，并打印完整读数）。
- **Lesson**: 「引用形态是否参与缓存」不能靠类比推断（base64 图 M-V.3 也是实测出来的）：file_id 与图 URL 一样可缓存，而且更稳——id 变了前缀照样命中，所以 ResolveFiles 的过期重传不会让用户多付一次 cache miss。
- **Refs**: `docs/prd/archive/feature-deepseek-app-api.md` §3.2 D-F6、§5 开放问题②；`pkg/deepseek/resolve.go`；`docs/prd/archive/feature-vision.md` M-V.3（base64 图先例）。
