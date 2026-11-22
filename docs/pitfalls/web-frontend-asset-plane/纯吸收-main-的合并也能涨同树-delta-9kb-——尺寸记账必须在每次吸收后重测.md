### 纯吸收 main 的合并也能涨同树 delta ~9KB——尺寸记账必须在每次吸收后重测
- **Saw**: c_6xktw4 首次吸收 main 后同树对照测得本卡 delta +17520B；同分支二次吸收 main（35 commits，本卡零新码）后重测，delta 变 +26664B——多出的 ~9KB 没有任何本卡提交。
- **Why**: 二次合并带入的 `session.go→title.go` 拆分等重构改变了跨包内联/链接形状：本卡在 `session/list` 里加的 `pricing.Cost` 调用与 main 新代码边相遇后，编译产物布局变化对两侧不对称。31MB 二进制上几个 KB 的链接器抖动是常态，「吸收前后 delta 不变」不成立。
- **Fix**: 尺寸记账的口径从「交付时测一次」改为「每次吸收 main 后重测」；c_6xktw4 收尾轮以二次合并后的终测数字（+26664B）为准写 PRD §13.3 与基线棘轮注释，首测数字留作过程记录并注明差异来源。
- **Lesson**: 同树对照 delta 只在「树没动过」的前提下可复用。凡分支吸收了 main（哪怕全是别人的提交），交付前必须重跑同树对照——否则要么多记账（虚报超配），要么少记账（漏掉真实增长）。
- **Refs**: `scripts/web-size-baseline.txt` 2026-11-21 c_6xktw4 ratchet 注释；`docs/prd/feature-web-frontend.md` §13.3 c_6xktw4 行。
