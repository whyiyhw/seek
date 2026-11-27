### 成功路径上的 [hint:] 提示不教学——40 会话实测 3,871 次触发、0 效果，删掉 67% 换成一次性限流
- **Saw**: bash 工具在命令成功后追加 `[hint: …]` 指向专用工具。40 会话 6,474 次调用触发 3,871 次、410,967 字节（工具结果字节 3.1%，进 history 每请求重发）。下一跳照办率：grep 1,447 → 0.6%、git 841 → 1.3%、list_dir 291 → 0%、find 14 → 0%；read 13.3% 但无提示基线 12.7%（差 0.6pp）。会话内前后各 5 次对照四类全负或平（cd 19.4%→20.8%）。
- **Why**: 被拒命令的 denial hint 承重；成功命令无需改——提示在结果尾部，选型由任务语义定。同族：提示词层禁止压不动行为，描述/schema 才行。
- **Fix**: 删 grep / git / list_dir / find 四类（2,593 次触发 = 67%）；read 与 cd 加每会话每类一次限流（`internal/tools/bash/advisory.go`，挂 Tool 不挂包级，nil limiter = 不限流）。最坏单会话 cd 574 次。被删类别由 `TestBashAdvisory_DeletedClassesStayDeleted` 钉死。
- **Lesson**: 发教学型提示先问受众在不在：命令失败→在；命令成功→纯成本。只看照办率会被基线骗（13.3% vs 12.7%），须测无触发基线或做会话内前后对照。
- **Refs**: `internal/tools/bash/hint.go`、`internal/tools/bash/advisory.go`、`eval/cases/bash-overreach/`、`docs/pitfalls/eval/explicit-bash-bans-in-prompts-…`
