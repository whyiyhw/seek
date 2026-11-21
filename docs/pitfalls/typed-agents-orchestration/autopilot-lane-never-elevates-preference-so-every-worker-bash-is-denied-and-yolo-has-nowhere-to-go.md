### `seek autopilot run` 车道从不升 pref，且 `--yolo` 没有可表达的位置——产线 subagent 的 bash 全被拒

- **Saw**: 2026-11-21（c_2aymtn）真跑 `seek autopilot run "<goal>"`：worktree 子代理 `edit`/`write` 成功，`bash` 一律返回 `permission denied: bash is gated; re-run seek with --yolo …`，worker 因此跑不了 `go test ./...`，只能静态自查后回报"完成"。
- **Why**: `run()` 只在 `goalMode` 分支 `policy.SetPref(permission.PrefYolo)`（`cmd/seek/main.go:1745-1750`）；`autopilotMode` 分支（`:1377-1388`）没有，pref 停在 `PrefDeny`，子代理经 `ParentPolicy.Spawn` 继承（只降不升）。想用 `--yolo` 补也补不上：flag.Parse 在第一个非 flag 参数处停止，`autopilot` 是位置参数 —— `seek autopilot run "g" --yolo` 把 `--yolo` 折进 goal 字符串（实测 Report 里 goal 就是 `noop gauge --yolo`）；`seek --yolo autopilot run "g"` 则因 `autopilotMode` 判的是 `os.Args[1]`（`:1241`）认不出车道，掉回 TUI（无 TTY 直接报错）。两种形状实测都不通。
- **Fix**: 未修 —— c_2aymtn 判定为 wiring 缺口，修复另开卡。候选修法二选一：autopilot 分支照 goal 车道先例 `SetPref(PrefYolo)`（bash 仍被 no-remote 闸 `LaneAutopilot` 约束），或把 autopilot 收进 flag.Parse 之后的子命令表。
- **Lesson**: 无人值守车道的 pref 必须由车道自己升；「子命令在前」与「全局 flag 在后」在 Go 的 flag 语义下互斥，这个互斥只能靠**实测两条命令形状**发现，读代码不容易看出来。顺带：该状态下 worker 仍能改文件，"done" 与 "自己验过" 是两件事。
- **Refs**: `cmd/seek/main.go:1241-1245`（车道探测）、`:1377-1388`（autopilot 分发）、`:1740-1752`（goal 才升 yolo）；`internal/routines/tick.go:93`（cron 定时形态，同一个洞）；`internal/subagent/manager.go` `ParentPolicy.Spawn`；同族坑 `docs/pitfalls/tooling/手测脚本的「等人按 Enter」在无人值守车道会变成假绿…`。
