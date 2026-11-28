### 面向模型的 id 参数必须接受模型实际看得到的形态——agent 脚注只露 sub-sid 短后缀
- **Saw**: 续话让模型回传 sub-sid 时，它只有结果脚注「— sub-sid: a1b2c3」（随机短后缀）；按全 id 精确匹配，真实对话必报 `unknown_session`，测试用全 id 却全绿。
- **Why**: `FormatCompleted/FormatContinued` 脚注给人读，展示 `shortSid`；工具 schema 若说「传 sub-sid」而模型只能引用短形态，功能在 eval/实战中差最后一步。
- **Fix**: `Manager.Continue` 先全 id 精确匹配，再按 `"-<suffix>"` 在本项目索引内唯一后缀解析；多命中报 `unknown_session` 并列出全 id 候选。schema 明说两种形态都收。
- **Lesson**: 给模型开的 id 入口，收它看得见的形态（脚注/面板展示什么就解析什么）；只用全 id 的契约要在测试里用「模型视角的 id」走一遍才算数。
- **Refs**: `internal/subagent/manager.go`, `internal/tools/agent/agent.go` (session_id schema), `internal/subagent/manager_continue_test.go:TestContinue_ShortSidResolution`
