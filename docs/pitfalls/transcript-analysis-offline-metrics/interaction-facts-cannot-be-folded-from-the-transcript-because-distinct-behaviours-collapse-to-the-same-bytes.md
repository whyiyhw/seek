### 交互事实（插话/排队/转向/撤回）无法从 transcript 折叠——必须侧车 + 独立 CLI
- **Saw**: 要给「智能引导」定阈值，需要知道用户多频繁地中途改主意、用哪种机制、有多少真送达模型。查遍 `~/.seek/` 发现：`ui-trace.jsonl` 不存在，`friction.Fold` 只看工具调用，会话 JSONL 里一条也读不出来。
- **Why**: 三种交互在磁盘上**同形或无形**——(1) 边界注入的 user 消息与转向重发的 user 消息是字节相同的 `user` 行；(2) 撤回（空 Enter）什么都不写；(3) Esc 取消的轮次被丢弃，只留一个可能存在的孤儿 tool_call。所以「从 transcript 折叠」这条路在这里不成立，这不是偷懒，是信息缺失。`internal/sessmeta` 的 rename 覆盖层是同型先例（「不可从任何 transcript 派生」）。
- **Fix**: 新增 `internal/uitrace`（append-only JSONL，`~/.seek/ui-trace.jsonl`，全局单文件；只记 len 不记文本）+ `internal/tui/trace.go` 的 `traceEvent`（nil-safe、吞错误——轨迹坏了不能坏按键）+ `seek ui-trace` CLI（`internal/uitracecli`，`--rows/--since/--json`，口径唯一定义在 `uitrace.Fold`）。`--no-save` 下不写（无 session id 可归属，而该文件的价值就在跨会话比率）。
- **Lesson**: 判定「能不能从 transcript 折叠」要看**信息是否存在**，不是看「能不能写个 fold 函数」。当两个不同行为落到同一字节形状，或者某个行为不落任何字节，侧车就是唯一诚实的选择——此时必须在包注释里写明是哪一个行为缺失，否则下一个维护者会以为是偷懒重写的第二份状态。
- **Refs**: `internal/uitrace/uitrace.go`（包注释的完整论证）、`internal/tui/trace.go`、`internal/uitracecli/`、`internal/paths/paths.go:UITraceLog`、`internal/sessmeta/sessmeta.go`（同型先例）
