### web-gate 余量按「页面字节」估会错：embed 段是 ~4KB 台阶，+30B 就能跳 +4KB

- **Saw**: 本卡页面只加两行 CSS（`webfrontend.html` 276978→277342B，净 +364B），`bash scripts/web-gate.sh` 却报 `built 32024673B → 32028793B`（+4120B），把只剩 3964B 的 pin 余量一次吃穿。
- **Why**: `go:embed` 把页面塞进一个数据段，链接器按 4KB 页对齐落段；页面字节跨过某个边界，整段就多一页。同路径对照实测（同 worktree、同 toolchain、stash 往返）：页面 +20B → 构建体 32024673B 不变；+30B → 32028745B（+4072B）；+200B / +1000B / +3000B 都在 32028753–32028817B 之间。也就是说「余量 − 页面增量」不是可加关系，是台阶函数。
- **Fix**: 本卡抬 pin 只认自己的实测 delta（31823837 → 31827957，注释记了同路径非 `-trimpath` 与 `-trimpath` 两次读数）。判据本身没改：预算 204800B 挂在 pin 上，台阶只是让「小改动也会撞墙」。
- **Lesson**: 估 web-gate 余量要用「改完真跑一次 gate」，不能拿页面 diff 字节数当增量。余量只剩个位数 KB 时，任何页面改动都应当作「可能直接超 budget」来排期，而不是「先加几十字节再说」。
- **Refs**: `scripts/web-gate.sh` `build_size`、`scripts/web-size-baseline.txt`（2026-11-28 c_lf2qrc 条）、卡 c_lf2qrc。
