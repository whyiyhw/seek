### 纯吸收 main 的合并也能涨同树 delta ~9KB——尺寸记账必须在每次吸收后重测
- **Saw**: c_6xktw4 首次吸收 main 后同树对照测得本卡 delta +17520B；同分支二次吸收 main（35 commits，本卡零新码）后重测变 +26664B——多出的 ~9KB 没有任何本卡提交。
- **Why**: 二次合并带入的 `session.go→title.go` 拆分等重构改变跨包内联/链接形状：本卡在 `session/list` 加的 `pricing.Cost` 与 main 新代码边相遇，编译产物布局变化不对称。31MB 二进制上几 KB 链接器抖动是常态，「吸收前后 delta 不变」不成立。
- **Fix**: 尺寸记账从「交付时测一次」改为「每次吸收 main 后重测」；c_6xktw4 收尾以二次合并后终测（+26664B）写 PRD §13.3 与基线棘轮注释，首测留作过程记录。
- **Lesson**: 同树对照 delta 只在「树没动过」前提下可复用；分支吸收了 main（哪怕全是别人的提交），交付前必须重跑——否则多记账（虚报超配）或少记账（漏真实增长）。
- **Refs**: `scripts/web-size-baseline.txt` 2026-11-21 c_6xktw4 ratchet 注释；`docs/prd/feature-web-frontend.md` §13.3 c_6xktw4 行。
