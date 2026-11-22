### 成功路径上的 [hint:] 提示不教学——40 会话实测 3,871 次触发、0 效果，删掉 67% 换成一次性限流

- **Saw**: bash 工具在命令**成功**后会给结果尾部追加 `[hint: …]`，指向更该用的专用工具（ls→list_dir、
  cat→read、grep→grep tool、git→git tool……）。40 个会话 6,474 次调用里触发 3,871 次、共 410,967 字节
  （全部工具结果字节的 3.1%，且每条都进 history、每个后续请求重发一遍）。按类测「下一跳是否照办」：
  grep 1,447 次 → 0.6%、git 841 → 1.3%、list_dir 291 → 0%、find 14 → 0%。`read` 看起来 13.3% 像有效果，
  但**无提示时下一跳本来就是 read 的基线率是 12.7%**——差 0.6pp。再换更干净的口径（同一会话内、触发点
  前后各 5 次工具调用的该行为占比）四类全是负的或平的：grep 1.0%→0.3%、git 0.7%→0.5%、
  list_dir 0.7%→0.1%、read 7.6%→7.4%、cd 19.4%→20.8%（+1.4pp，5 次窗口内属噪声）。
- **Why**: 提示的价值取决于**受众是否存在**。被**拒绝**的命令（plan-analyze denial hint）必须改，
  所以那段提示是承重的；**成功**的命令不需要改，提示只是评论——模型在做完一件事的瞬间拿到「你本可以
  用别的工具」，既不改变已完成的动作，也不改变下一次的选型（选型由任务语义决定，而提示在结果尾部、
  离下一次生成最远）。同族前车之鉴：`docs/pitfalls/eval/explicit-bash-bans-in-prompts-underperform-
  tool-description-guidance`——提示词层面的禁止也压不动行为，描述/schema 才行。
- **Fix**: 删掉 grep / git / list_dir / find 四类（2,593 次触发 = 67%），保留 read 与 cd；剩下两类加
  **每会话每类一次**的限流（`internal/tools/bash/advisory.go`，挂在 Tool 上而不是包级全局——测试与并行
  会话不共享；nil limiter = 不限流，供零值 Tool 用）。实测最坏的单会话 cd 提示触发 574 次。
  被删的类别用测试钉死（`TestBashAdvisory_DeletedClassesStayDeleted`），重新加回来必须带新测量。
- **Lesson**: 写「教学型」提示前先问一句**受众在不在**：命令失败了、动作还没发生 → 受众在，提示可能救
  回一轮；命令成功了、动作已完成 → 受众不在，提示是纯成本。要测这类机制，**光看照办率会被基线率骗**
  （read 的 13.3% vs 12.7%），必须同时测「没触发时该行为的基线率」或做会话内前后对照。
- **Refs**: `internal/tools/bash/hint.go`（实测表格在 bashAdvisory 上方）、`internal/tools/bash/advisory.go`、
  `eval/cases/bash-overreach/`（同族：bash 开局的度量）、`docs/pitfalls/eval/explicit-bash-bans-in-prompts-…`
