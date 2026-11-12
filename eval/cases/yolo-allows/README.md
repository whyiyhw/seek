# yolo-allows

**Probes**: `--yolo` 把初始 pref 升为 `PrefYolo` 后 **bash 全放行**——`echo` 这类非白名单命令正常执行并带回输出，而不是 `ErrDenied` 工具结果。与 `print-deny-semantics`（无 `--yolo`，bash 被挡）互为对照，两例合起来钉死 pref 两端的 bash 门禁行为。

**来源**: 卡片 c_7hudp6 动工前实测（2026-11-12，"--yolo 实测全放行（bash 正常执行）"），结论回填 `docs/prd/feature-remote-control-plane.md` §6 已验证② / D-C5（binding 显式 `yolo:true` = exec 时加 `--yolo` 的信任路径）。

**代码锚点**:
- `cmd/seek/main.go` `if *yolo { initialPref = permission.PrefYolo }`——紧跟 `initialPref := permission.PrefDeny` 的唯一升级口。

**形态**: **无 runner.sh**——harness 默认调用本就带 `-yolo -no-save`，正好是被测形状（顺带回归默认调用路径本身）。

**指标**:
- `min_bash_calls = 1` —— bash 真被调用（显式指令）。
- `max_denied_tool_ends = 0` —— 放行：无任何 `permission denied` 工具结果。
- `min_agent_ends = 1` —— run 正常收尾。

**注意**: LLM 行为探针，随机性同 `eval/README.md` 总则；`echo` 无副作用，不需要 setup/teardown。
