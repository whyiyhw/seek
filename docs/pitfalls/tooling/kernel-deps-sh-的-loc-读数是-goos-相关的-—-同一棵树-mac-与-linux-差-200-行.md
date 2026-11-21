### `kernel-deps.sh` 的 LOC 读数是 GOOS 相关的 — 同一棵树 mac 与 Linux 差 200 行
- **Saw**: 拿卡片里记的「内核 10541 LOC」对不上现读数 11168，一度怀疑是清单漂移或自己算错；`git archive 1a8e124` 复跑后 mac 读数正好 10541，而同一棵树 `GOOS=linux` 读 10742。
- **Why**: 报告用 `go list -f '{{range .GoFiles}}…'` 数**非测试文件**，`.GoFiles` 是按**当前目标平台**过滤后的集合。`internal/sandbox`（`sandbox_{darwin,linux,other}.go`）和 `internal/tools/bash`（`bash_{unix,windows}.go`、`proc_{linux,other_unix}.go`）都有对称的构建约束分支，于是行数随 `GOOS` 变——darwin 11168 / linux 11369，差 201 行全在这两个包里。
- **Fix**: 不改脚本（报告自身没错，是口径没写明）；`docs/prd/v8.md` §9.6 的读数表**双列记录** darwin / `GOOS=linux`，并注明复跑命令。卡片与 PRD 引用这个数时一并写平台。
- **Lesson**: 任何"数非测试行数"的读数工具，跨机器比对前先固定 `GOOS`（CI 跑 Linux，本机多半 darwin）。差几百行先查构建约束，别先怀疑清单或自己的算术——**同一棵树在两个平台上本来就会报两个数**。顺带：违例条目**集合**本身是 GOOS 无关的（改动前 9 条、改动后 1 条，darwin/linux/windows/freebsd 逐字相同），只有 LOC 变。
- **Refs**: `scripts/kernel-deps.sh:92`（`{{range .GoFiles}}`）、`docs/prd/v8.md` §9.6 读数表、card c_3acz3q。
