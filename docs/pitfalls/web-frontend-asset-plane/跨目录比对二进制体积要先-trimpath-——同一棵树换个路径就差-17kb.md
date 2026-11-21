### 跨目录比对二进制体积要先 `-trimpath`——同一棵树换个路径就差 17KB
- **Saw**: 2026-11-20 c_7lsngm 收尾时用两个临时目录（`/tmp/sz` 与 `/tmp/p1`）分别构建 main 作参照，同一个 commit 得到 29258219B 与 29258227B；再把同一个 main 解到 `/tmp/averyvery...experiment` 试，涨到 29275459B——**零代码差异，17KB 的「增长」**。跨目录对照一度把本卡净增量出 13163B / 17806B 两个数（真值 13195B）。
- **Why**: Go 把每个包的**绝对导入路径**编码进构建产物（行号表/类型元数据里的文件名字符串）。`web-gate.sh` 比较基线时在同一棵树、同一路径上构建，所以不受影响；而「把 main 解到另一个目录当参照」这种卡内自测常用于拿 delta，路径长度就混进了差值——**而且参照树与工作树的路径长度差是不对称的**，同一个 commit 在短路径与长路径下可以差 17KB。
- **Fix**: 参照测量统一加 `-trimpath`（与工作树同一套 build flags），跨目录对照即稳定（实测两次独立复现都是 29169937B）：`GOTOOLCHAIN=go1.25.8 GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -buildvcs=false`。本卡的 delta 记账即按 trimpath 口径（main 29169937B → 本卡 29183132B，+13195B）。**gate 本身不改**——它同路径比对，没有这个问题。
- **Lesson**: 体积 delta 只有在「同 flags + 同工具链 + 同路径形状」三者都对齐时才可比；跨目录自测时 `-trimpath` 是把路径变量消掉的开关。和上一条（工具链 skew）是同一类错误的两个方向：**测量环境变了，delta 就不是 delta**。
- **Refs**: `scripts/web-gate.sh` `build_size`（无 `-trimpath`，同路径语义）；`docs/prd/feature-web-frontend.md` §13.3 记账口径。
