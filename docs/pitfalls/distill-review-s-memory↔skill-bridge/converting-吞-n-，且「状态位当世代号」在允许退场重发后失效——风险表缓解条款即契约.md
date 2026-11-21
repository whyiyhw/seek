### converting 吞 `n`，且「状态位当世代号」在允许退场重发后失效——风险表缓解条款即契约
- **Saw**: 验证会话抓到 ⚡ review converting 态只认 q/Esc/Ctrl+C，`n` 被吞，与 PRD §11「`n` 随时可退」冲突（测试甚至断言 n 无效——把违约钉成了绿灯）。修的时候暴露第二层：上一条坑里「`distillConverting` 天然充当世代位」的论证在 n 退场合法化后破产——n 清 flag、同 idx 再按 s 重新置位，旧在飞结果落地时 idx+converting 全对上，会被当新结果放行。
- **Why**: §11 风险表缓解列（「`n` 随时可退；转换只付一次」）写进 PRD 就是接口契约，实现/测试与它冲突时违约的是实现不是文档；而「复合状态位充当世代号」只在「该位只被单调事件清零」时成立，一旦出现「清零后可原位重置」的路径（retreat+refire 同 idx），就必须换成真正的单调计数器。
- **Fix**: 卡 `c_2lbcck`（M-B2 返工，2026-11-19 拍板）：converting 态 `n` → 回候选提示态 + `distillConvertGen++` 作废在飞调用；stale 守卫加第四元 `msg.gen != m.distillConvertGen`；「只付一次」定为按候选 idx 缓存 `err==nil` 结果（PASS/GREY 回放预览、REJECT 回放拒绝行、不重付；err 不缓存，保住「retry with s」）。测试 `TestDistillConvert_NDuringConvertingBacksOutOrphansResult` / `TestDistillConvert_SecondSOnSameIdxReplaysFromCache`。
- **Lesson**: ①PRD 风险表的缓解条款按契约验收，不是背景散文；②拿状态位当 stale 世代号前先问它有没有「清零后同 key 重新置位」的路径，有就上单调计数器；③「只付一次」类承诺要有可机械验证的口径（按什么键、缓存什么、err 入不入缓存），否则实现各自解读。
- **Refs**: `internal/tui/distill.go` `handleDistillKey`（converting 分支）/`handleDistillConvertDone`/`routeConvertOutcome`，`docs/prd/feature-memory-skill-bridge.md` §3.1/§3.2/§11/§10 #13-14。
