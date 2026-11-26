### `kernel-deps.sh` 的 LOC 读数是 GOOS 相关的 — 同一棵树 mac 与 Linux 差 200 行
- **Saw**: 卡片记的「内核 10541 LOC」对不上现读数 11168；`git archive 1a8e124` 复跑后 mac 读数正好 10541，同一棵树 `GOOS=linux` 读 10742。
- **Why**: 报告用 `go list -f '{{range .GoFiles}}…'` 数非测试文件，`.GoFiles` 是按当前目标平台过滤的集合；`internal/sandbox`（`sandbox_{darwin,linux,other}.go`）与 `internal/tools/bash`（`bash_{unix,windows}.go`、`proc_{linux,other_unix}.go`）有对称构建约束分支——darwin 11168 / linux 11369，差 201 行全在这两个包。
- **Fix**: 不改脚本（报告自身没错，是口径没写明）；`docs/prd/v8.md` §9.6 读数表双列记录 darwin / `GOOS=linux` 并注明复跑命令；卡片与 PRD 引用时一并写平台。
- **Lesson**: 「数非测试行数」的读数工具跨机器比对前先固定 `GOOS`（CI 跑 Linux，本机多半 darwin）；差几百行先查构建约束——同一棵树在两个平台本来就会报两个数。违例条目集合本身 GOOS 无关（改动前 9 条、改动后 1 条，darwin/linux/windows/freebsd 逐字相同），只有 LOC 变。
- **Refs**: `scripts/kernel-deps.sh:92`（`{{range .GoFiles}}`）、`docs/prd/v8.md` §9.6 读数表、card c_3acz3q。
