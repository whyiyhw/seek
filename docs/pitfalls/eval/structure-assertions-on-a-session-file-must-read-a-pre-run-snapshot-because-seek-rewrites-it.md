### 「先跑再查会话文件」的结构断言查的是被改写过的那份 —— 跑之前先快照
- **Saw**: compact-ab 断言「arm B 种子会话是 `/compact` 摘要对」：读 `seek -resume arm-b-summary` 之后的会话文件，期望 2 条消息、首条以 `Here is a summary of our earlier conversation.` 开头；实际 5 条、首条 `Language: Match the user's input language…`——红。
- **Why**: seek 每次保存整份重写：system prompt 重新 materialise 成第一条（`session.Save` 落 agent 完整历史，其以 system 开头），新消息再追加——种子文件跑完一轮不再是种子形状。非 bug：会话文件代表「现在的对话」。
- **Fix**: 断言对象是想证明的时刻：跑前 `cp` 种子快照，结构断言读快照；「为何必须快照」写进 runner 注释，防后人优化成直接读 `$arm_b`。
- **Lesson**: 断言输入形状前先问「这文件在断言时还是输入吗」——被被测程序写权的文件（会话/日志/缓存/状态）跑完即输出；快照成本一次 `cp`，误判成本一整轮红/绿反转。
- **Refs**: `eval/cases/compact-ab/runner.sh`（`arm_b_seeded` 快照）、`internal/session/session.go:SaveTo`、`pkg/agent/agent.go:Messages`
