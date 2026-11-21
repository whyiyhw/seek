### Subagent continuation must mint a NEW sub_sid — same-id resurrection corrupts the fold
- **Saw**: 2026-11-13 P1-b 设计（卡 c_u4ugnu）：续话最自然的形态是照抄 CC 的 SendMessage——同 agent id 再入追加指令。但按同 id 二写 `started` 事件，fold 后该子永远停在旧终态，续话运行中 /agents 面板显示 completed。
- **Why**: `foldEvents` 对同一 sub_sid 的重复 `started` 只保留首见（视为 sub_sid 空间碰撞），且 `types.go` 契约是「一个 sub_sid 恰好一个终态，终态后不再有事件」。同 id 复活要求改 fold / orphan 扫描 / TUI 三处语义，且 `writeSubagentTranscript` 同路径覆写会丢掉旧 transcript 历史。
- **Fix**: `Manager.Continue` 每次续话铸新 sub_sid，血统记在 `started` 事件的 `resumed_from` 字段（fold 透出 `Subagent.ResumedFrom`）；新 transcript 自包含（prior messages + 续话轮次），旧文件零改动。
- **Lesson**: 事件溯源索引里「id = 单生命周期」是不变式；复活/续话类需求一律用「新 id + lineage 字段」表达，别让一个 id 活第二段人生。
- **Refs**: `internal/subagent/manager.go` (Continue), `internal/subagent/index.go` (foldEvents duplicate-started branch), `docs/prd/feature-orchestration.md` §4 P1-b
