### web-gate 的 size 基线钉在「哪个工具链量的」上——±1.8MB 的 skew，棘轮要按 delta 加不要按绝对值重测
- **Saw**: 2026-11-19 c_x2au4t：本机 go1.27.3 超基线 +440KB，未改动 main@2e7ad39 亦 +426KB；`GOTOOLCHAIN=go1.25.8`（CI）同树 29.1MB，低 1.37MB。
- **Why**: 基线是某 Go 版本编译一次的绝对值，小版本差 MB 级（1.25.8→1.27.3 同源 ~1.8MB）；基线不含工具链信息，`--update-baseline` 只在环境不变时成立。
- **Fix**: 棘轮按 delta 加（+14109B/go1.27、+13557B/go1.25.8，配额 ≤30KB），基线注释记口径 `GOTOOLCHAIN=go1.25.8 bash scripts/web-gate.sh`；gate 未动。
- **Lesson**: 跑 gate 先对齐 CI 工具链线，不同线只按 delta 说话。
- **2026-11-21 c_o3osoh**: `toolchain go1.25.8` 三关皆死：①`GOTOOLCHAIN=auto` 只升不降；②go1.26+ 行 ≤ go 指令非法（`go: updates to go.mod needed; go mod tidy`）；③依赖地板 charm.land/glamour/v2 声明 `go 1.25.8`。复现 CI 读数只能 `GOTOOLCHAIN=go1.25.8`；toolchain 行是下限不是钉子。
- **2026-11-20 c_o3osoh**: 全仓 1.27.3 化——go.mod 1.25.8→1.27.3、CI `go-version: '1.27.3'`、基线重铸 31,205,746B，本地默认即闸线；旧读数=迁移前提交+`GOTOOLCHAIN=go1.25.8`。旧线棘轮史见基线注释（c_x2au4t/c_txz6vz/c_oyp2qf）；两线 delta 差 <500B；重铸 +687,839B=1,810,518B−1,122,679B。
- **Refs**: `scripts/web-size-baseline.txt`、`scripts/web-gate.sh` `build_size`、`.github/workflows/ci.yml` `go-version: '1.27.3'`。
