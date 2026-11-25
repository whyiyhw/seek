### 「先跑再查会话文件」的结构断言查的是被改写过的那份 —— 跑之前先快照
- **Saw**: compact-ab 断言「arm B 的种子会话就是 `/compact` 留下的摘要对」：读 `seek -resume arm-b-summary` **之后**的会话文件，期望 2 条消息、第一条以 `Here is a summary of our earlier conversation.` 开头。实际拿到 5 条，第一条是 `Language: Match the user's input language…` —— 断言红了。
- **Why**: seek 每次保存会话都会整份重写：system prompt 重新 materialise 成第一条消息（`session.Save` 落的是 agent 的完整历史，而 agent 的历史以 system 开头），本轮新消息再追加。种子文件跑完一轮就不再是种子形状。这不是 seek 的 bug——会话文件本来就代表「现在的对话」，不是「当初喂进去的是什么」。
- **Fix**: 断言对象是想证明的**那个时刻**：跑之前 `cp` 一份种子快照，结构断言读快照。同时把「为什么必须是快照」写进 runner 注释，因为下一个改这段的人很容易把它优化成「直接读 $arm_b，省一次 cp」。
- **Lesson**: 断言「输入形状」时，必须先问「这个文件在断言时还是输入吗」。任何被被测程序拥有写权的文件（会话、日志、缓存、状态文件）都会在跑完之后变成输出；对它的断言是在断言输出，不是输入。快照成本一次 `cp`，误判成本是一整轮红/绿反转。
- **Refs**: `eval/cases/compact-ab/runner.sh`（`arm_b_seeded` 快照）、`internal/session/session.go:SaveTo`、`pkg/agent/agent.go:Messages`
