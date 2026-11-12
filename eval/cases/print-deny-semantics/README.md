# print-deny-semantics

**Probes**: print 模式（`-p`，无 TUI）下的默认权限语义——初始 pref 即 `PrefDeny`（非 ask、非被迫 yolo），精确挡面 = **bash 全挡**（ReadOnly 白名单除外）＋ out-of-CWD 读写挡，**in-CWD write 放行**；拒绝以 `ErrDenied` **工具结果**返回（非致命），agent 自行调整继续，进程 **exit 0 不挂起**。

**来源**: 卡片 c_7hudp6 动工前实测（2026-11-12，实测 `pwd` 级 bash 也被挡、agent 收到带 hint 的拒绝后继续完成任务、exit 0），结论回填 `docs/prd/feature-remote-control-plane.md` §6 已验证② / D-C5。这是远程平面 v1 默认体验（"能读能写项目目录、不能跑任何 shell"）的行为基线。

**代码锚点**:
- `cmd/seek/main.go` `initialPref := permission.PrefDeny`（print 路径无 askFn，注释明说 "can't realistically interrupt to ask"；`--yolo` 才升 `PrefYolo`）。
- `internal/permission` `ErrDenied = errors.New("permission denied")`——`prefGate` 对危险动作的返回值，经 tool_end 的 `error` 字段上浮（`denied_tool_ends` 指标匹配的就是这段文本）。
- `internal/tools/bash/readonly.go` 白名单（go/npm/yarn/make/which/type/command）——prompt 选用 `touch` 正是为了**不在**白名单上，保证被挡。

**形态**: `runner.sh` 复制默认调用但**去掉 `-yolo`**（其余含 `-no-save` 不变；本例不锁 session_id）。prompt 两步：bash `touch`（预期被拒）→ write 工具建 in-CWD 文件（预期放行）。`teardown.sh` 记录离线金标 `bash_marker=0 / write_marker=1`。

**指标**:
- `min_denied_tool_ends = 1` —— bash 被挡且以工具结果形式返回（不是致命错误）。
- `min_write_ok = 1` ＋ `max_write_refusals = 0` —— in-CWD write 放行。
- `max_nonzero_exits = 0` —— exit 0；"不挂起"由 harness 本身完成本行隐式证明（挂起 = 整个 eval 卡死，立即可见）。

**注意**: 这是 LLM 行为探针，带随机性——单次 FAIL 是信号不是证明（见 `eval/README.md`）。模型理论上可能不听指令跳过 bash（denied=0 假 FAIL）；prompt 已用显式工具名+命令把该风险压到最低。
