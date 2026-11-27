### Same session re-entering the distill window double-counted recurrence
- **Saw**: 卡 c_jqwd72 确认：`StrengthenDraft` 无条件累计每次 merge；同一 session 重进窗口（mtime 在 watermark 后：state 重置、批次重叠）把单 session draft 推到 recurrence 2 → "reviewable"，击穿两独立 session 闸门。Fix 卡 c_jdwywj。
- **Why**: 窗口按 mtime 划分（`prev < UpdatedAt ≤ watermark`，`internal/skilldistill/auto.go:runBatch`）；watermark 是唯一挡住已蒸馏 session 进后续批次的机制，而它是可重置 state 文件——下游无幂等保护。
- **Fix**: `StrengthenDraft` 按 SessionID 去重（merge 咽喉点，覆盖 match 与写竞争 fallback）：`src` 的 SessionID 已在 `sources` 里则全 no-op——不计数、不追加 source/evidence/trace、不 bump UpdatedAt。空 SessionID 不去重。
- **Lesson**: 窗口/watermark 只保证 at-most-once 批次划分，不保证 at-most-once 计数；recurrence 类计数必须在自增处按 session 身份去重——watermark 是防御层，不是不变量。
- **Refs**: `internal/skillmgr/draft.go:StrengthenDraft`, `internal/skilldistill/auto_test.go:TestAutoPath_SameSessionReentryNoRecount`, `docs/prd/feature-skill-distill.md` §4
