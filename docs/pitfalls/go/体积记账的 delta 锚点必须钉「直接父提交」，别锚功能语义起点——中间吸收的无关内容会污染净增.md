### 体积记账的 delta 锚点必须钉「直接父提交」，别锚功能语义起点——中间吸收的无关内容会污染净增
- **Saw**: 2026-11-20 c_n37tnr 收尾轮复测：上会话给 PRD §13.3 记的「+52 059B（a5b420f→本卡终态）」两个口径都对不上——a5b420f→终态实测 +59 966B（多 12 283B），直接父 031f099→终态实测 +47 683B（少 4 376B）。
- **Why**: 基线 031f099 是 merge，除 a5b420f 的 web 半边还吸收了 c_idisqf 的 compact_rescue 等（`git diff a5b420f 031f099` 有 1119 行插入），按语义起点量净增会把别人卡的内容记进自己账上；且 52 059B 两个口径都不再现，说明测量时树的状态与终态不一致。
- **Fix**: 收尾轮以 `git worktree add --detach <tmp> <parent>` 逐端点同工具链（GOTOOLCHAIN=go1.25.8）构建实测，PRD §13.3/CHANGELOG 改成 +47 683B 并写明口径（031f099→终态）与 a5b420f 口径差值来源；commit d1dc710。
- **Lesson**: 净增是 diff 概念，锚点写错数字再准也是别人的账；写进 PRD/CHANGELOG 的体积数字必须同时写「锚点提交 + 工具链」，缺一个下个会话就得重测。
- **Refs**: `docs/prd/feature-web-frontend.md` §13.3 c_n37tnr 行；上条「web-gate 的 size 基线」（工具链侧同族坑）。
