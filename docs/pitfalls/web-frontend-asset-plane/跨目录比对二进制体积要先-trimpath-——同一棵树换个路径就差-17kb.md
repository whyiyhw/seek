### 跨目录比对二进制体积要先 `-trimpath`——同一棵树换个路径就差 17KB
- **Saw**: 2026-11-20 c_7lsngm：`/tmp/sz` 与 `/tmp/p1` 分别构建 main，同 commit 得 29258219B/29258227B；解到长路径 `/tmp/averyvery...experiment` 涨到 29275459B——零差异 17KB。跨目录对照出过 13163B/17806B（真值 13195B）。
- **Why**: Go 把包的绝对导入路径编码进产物（行号表/类型元数据）；`web-gate.sh` 同树同路径不受影响，跨目录参照把路径长度混进差值，且路径长度差不对称。
- **Fix**: 参照测量统一 `-trimpath`（同套 build flags），跨目录稳定 29169937B：`GOTOOLCHAIN=go1.25.8 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false`；delta 按 trimpath 口径（29169937B→29183132B，+13195B）。gate 不改（同路径比对）。
- **Lesson**: delta 只在同 flags+同工具链+同路径形状时可比，跨目录自测用 `-trimpath`；与工具链 skew 同类——测量环境变了 delta 就不是 delta。
- **Refs**: `scripts/web-gate.sh` `build_size`（无 `-trimpath`，同路径语义）；`docs/prd/feature-web-frontend.md` §13.3 记账口径。
