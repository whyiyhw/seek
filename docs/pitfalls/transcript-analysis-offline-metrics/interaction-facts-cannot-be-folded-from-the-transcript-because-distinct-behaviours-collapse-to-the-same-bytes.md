### 交互事实（插话/排队/转向/撤回）无法从 transcript 折叠——必须侧车 + 独立 CLI
- **Saw**: 「智能引导」阈值需改主意频率/机制/送达率；`ui-trace.jsonl` 不存在，`friction.Fold` 只看工具调用，会话 JSONL 读不出。
- **Why**: 三种交互同形或无形：边界注入与转向重发的 user 消息是字节相同的 `user` 行；撤回（空 Enter）什么都不写；Esc 取消轮次被丢弃，只留可能孤儿 tool_call。`internal/sessmeta` rename 覆盖层为同型先例。
- **Fix**: `internal/uitrace`：append-only 全局单文件 `~/.seek/ui-trace.jsonl`（只记 len 不记文本）；`traceEvent`（`internal/tui/trace.go`，nil-safe 吞错误）；`seek ui-trace` CLI（`internal/uitracecli`；`--rows/--since/--json`；口径唯在 `uitrace.Fold`）；`--no-save` 不写（无 session id 可归属）。
- **Lesson**: 「能否从 transcript 折叠」看信息是否存在，非能否写 fold 函数；两行为同字节形状或某行为不落字节时，侧车是唯一诚实选择，须在包注释写明缺哪个行为。
- **Refs**: `internal/uitrace/uitrace.go`、`internal/tui/trace.go`、`internal/uitracecli/`、`internal/paths/paths.go:UITraceLog`、`internal/sessmeta/sessmeta.go`
