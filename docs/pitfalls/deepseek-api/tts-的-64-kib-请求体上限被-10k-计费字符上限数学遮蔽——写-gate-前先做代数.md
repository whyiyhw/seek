### TTS 的 64 KiB 请求体上限被 10k 计费字符上限数学遮蔽——写 gate 前先做代数
- **Saw**: S1 TTS 客户端实现时的本能 gate「拒绝超 64 KiB 的请求体」根本不可达：任何通过字符校验的 TTSRequest 都 marshal 不出超限 body（先写了 raw-UTF-8 长度预检，再被自己的注释里的数学推翻）
- **Why**: 字符上限钉死 rune 数 ≤10,000；JSON 每 rune 最坏膨胀是 1 字节控制字符转义成 6 字节（`\uXXXX`）→ 最坏 ~60 KB + ~50 字段开销，恒 < 64 KiB。raw 长度预检更死：4 字节 rune × 10,000 = 40 KB 封顶
- **Fix**: 不发 body gate；`TTSMaxBodyBytes` 保留为已发布约束的文档 + 带遮蔽数学推导的 tripwire 注释（`pkg/deepseek/tts.go`）。`TestTTS_RequestValidation` 钉住最坏膨胀 body 必须低于上限——将来加字段改变了每字符膨胀率，测试先红
- **Lesson**: 一个 API 上限能从另一个上限推导出来时，先代数后 gate——被遮蔽的上限检查是伪装成覆盖率的死代码。常量连同推导一起保留，下次加字段时数学会被重跑
- **Refs**: `pkg/deepseek/tts.go`, api-docs.deepseek.com/guides/tts
