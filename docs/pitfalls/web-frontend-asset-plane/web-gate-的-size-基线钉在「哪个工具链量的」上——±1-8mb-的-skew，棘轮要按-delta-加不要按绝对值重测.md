### web-gate 的 size 基线钉在「哪个工具链量的」上——±1.8MB 的 skew，棘轮要按 delta 加不要按绝对值重测
- **Saw**: 2026-11-19 c_x2au4t 验收 `scripts/web-gate.sh`：本机默认 go1.27.3 交叉构建报超基线 +440KB——但 `git stash` 后未改动的 main@2e7ad39 同样报 +426KB（零改动超线）；换 `GOTOOLCHAIN=go1.25.8`（CI 的版本线）重测，同一棵树只有 29.1MB，比基线低 1.37MB。
- **Why**: 基线数字是某台机器某个 Go 版本某次交叉编译的绝对值，而 Go 小版本间的代码生成差异能搬动 MB 级体积（1.25.8→1.27.3 同源差 ~1.8MB）。`--update-baseline` 的「重测绝对值」语义只在测量环境不变时成立；基线文件里没有任何工具链信息，skew 无法从文件本身看出来。
- **Fix**: c_x2au4t 的棘轮改按 delta 加（实测本卡净增 +14109B/go1.27、+13557B/go1.25.8，配额 ≤30KB 内），基线注释记下两侧工具链的实测数与复现口径（`GOTOOLCHAIN=go1.25.8 bash scripts/web-gate.sh` 对齐 CI）；未动 gate 脚本本身。
- **Lesson**: 体积门比较的是「同尺子下的差」不是「绝对数」——本地跑 gate 先确认工具链与 CI 同线，不同线就按 delta 说话；给基线写棘轮注释时把工具链版本当数据记进去。长期解（gate 内钉 GOTOOLCHAIN、或 CI 钉具体补丁版）是独立设计决定，不在渲染卡内顺手做。
- **Refs**: `scripts/web-size-baseline.txt` 尾注；`scripts/web-gate.sh` `build_size`；`.github/workflows/ci.yml` `go-version: '1.25'`。
