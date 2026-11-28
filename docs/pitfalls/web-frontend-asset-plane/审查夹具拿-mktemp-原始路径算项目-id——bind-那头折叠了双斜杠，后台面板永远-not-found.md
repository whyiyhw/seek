### 审查夹具拿 mktemp 原始路径算项目 id——/bind 那头折叠了双斜杠，后台面板永远 not found

- **Saw**: `web-page-review.sh` 的后台面板镜头（09/16/23/30）恒报 `subagent/list 失败: [-32003] project b04ef9670ef5e760 not found`，而记忆面板同轮显示 `1b6adfe0`——同一桩项目两个 id。
- **Why**: macOS `TMPDIR` 带尾斜杠，`mktemp -d "${TMPDIR}/…"` 原样返回 `…/T//seek-page-review.X`。夹具拿该串 `sha256[:16]` 当项目 id；`/bind` 后服务端走 `resolveProjectPath → filepath.Abs`，双斜杠被 Clean，`/state` 报的 `project_id` 是另一串的哈希。`paths.ProjectID` 契约即按字节哈希、不规范化（c_4c6dar），差一斜杠就是两个项目。
- **Fix**: `mktemp` 后立刻 `TMP="$(cd "$TMP" && pwd)"` 规范化（c_zib76h）。`cd && pwd` 只清多余分隔符、不解符号链接，与 `filepath.Abs` 的 Clean 同口径。
- **Lesson**: 「两端各算一遍同一路径的哈希」时路径串必须逐字节相同——在产生路径处规范化，别指望下游。不能换 `pwd -P` / `EvalSymlinks`：服务端不解符号链接，解了反而不对（`/var` → `/private/var`）。原注释「mktemp 返回物理路径」是错的。
- **Refs**: `scripts/web-page-review.sh`（`TMP=` 规范化处）、`internal/paths/paths.go`（`ProjectID` 契约）、`internal/controlplane/router.go`（`resolveProjectPath`）
