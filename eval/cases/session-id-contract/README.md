# session-id-contract

**Probes**: 远程控制平面赖以工作的 session-id 契约——`seek -p --json` 的 stdout JSONL **末行 `agent_end` 事件带顶层 `session_id` 字段**，且 `-resume <sid>` 续跑返回**同一 id**（session 文件复用，prefix cache 命中）。

**来源**: 卡片 c_7hudp6 动工前实测（2026-11-12，worktree 构建 + `SEEK_HOME` 隔离临时目录跑真 API），结论回填 `docs/prd/feature-remote-control-plane.md` §6 已验证①。本例把该事实锁成回归用例——契约回归（字段消失 / resume 换 id / 进程非零退出）时 FAIL。

**代码锚点**:
- `cmd/seek/main.go` `jsonLine.SessionID`（struct 定义，schema 注释声明 stable contract）与 `runJSON` 末尾 `end.SessionID = activeSession.ID` 的赋值——`-no-save` 时 `activeSession == nil`，字段合法缺席，所以本例**不能**用 harness 默认调用（带 `-no-save`）。
- `-resume` 加载路径：`cmd/seek/main.go` `store.Load(*resume)`（flag 定义 `-resume`，坏 id = exit 1 + stdout 空，平面侧自愈，不在本例范围）。

**形态**: `runner.sh` 链式两次调用（fresh → 提取 `agent_end.session_id` → `-resume <sid>`），共用隔离 `SEEK_HOME`（`eval/tmp/<case>/home/`，内含复制的 `config.json` 供鉴权，gitignored，`teardown.sh` 清理）。两条流拼接后交给 metric 提取器；退出码经 `runner_fact` 行上浮。

**指标**:
- `exact_agent_ends = 2` —— 两次调用各有 `agent_end`；任一进程中途死掉（无 `agent_end`）即 FAIL。
- `max_session_id_missing = 0` —— 每个 `agent_end` 都带 `session_id`。
- `exact_session_id_distinct = 1` —— resume 返回同一 id；换 id = 契约破坏。
- `max_nonzero_exits = 0` —— 两个进程都 exit 0。

**邻接已验证事实（本例不锁）**: 坏 id `-resume` = exit 1 + stdout 空（平面自愈责任）；mid-run error 无 `agent_end`（弃上下文重开，D-C3 已知边界）。见 PRD §6 已验证①。
