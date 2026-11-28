### converting 吞 `n`，且「状态位当世代号」在允许退场重发后失效——风险表缓解条款即契约
- **Saw**: ⚡ review converting 态只认 q/Esc/Ctrl+C，`n` 被吞，违反 PRD §11「`n` 随时可退」（测试曾断言 n 无效）；n 退场合法后「`distillConverting` 当世代位」失效——n 清 flag、同 idx 按 s 置位，旧在飞结果被当新结果放行。
- **Why**: 缓解条款即契约；状态位当世代号只在仅被单调事件清零时成立，可清零后原位重置（retreat+refire 同 idx）就得换单调计数器。
- **Fix**: 卡 `c_2lbcck`（M-B2，2026-11-19）：`n` → 回候选提示态 + `distillConvertGen++` 作废在飞调用；stale 守卫加第四元 `msg.gen != m.distillConvertGen`；「只付一次」= 按候选 idx 缓存 err==nil 结果（PASS/GREY/REJECT 回放；err 不缓存保 retry with s）。测试 `TestDistillConvert_NDuringConvertingBacksOutOrphansResult`/`TestDistillConvert_SecondSOnSameIdxReplaysFromCache`。
- **Lesson**: ①缓解条款按契约验收；②世代号前先查「清零后同 key 重置」路径，有则换单调计数器；③「只付一次」须可机械验证。
- **Refs**: `internal/tui/distill.go` `handleDistillKey`（converting 分支）/`handleDistillConvertDone`/`routeConvertOutcome`，`docs/prd/feature-memory-skill-bridge.md` §3.1/§3.2/§11/§10 #13-14。
