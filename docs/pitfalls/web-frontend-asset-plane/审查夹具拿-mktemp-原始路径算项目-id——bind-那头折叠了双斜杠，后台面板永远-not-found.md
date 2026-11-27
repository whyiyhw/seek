### 审查夹具拿 mktemp 原始路径算项目 id——/bind 那头折叠了双斜杠，后台面板永远 not found

- **Saw**: `web-page-review.sh` 的后台面板镜头（09/16/23/30）一直是 `subagent/list 失败: [-32003] project b04ef9670ef5e760 not found`，而记忆面板同一轮显示的是 `1b6adfe0`——同一个桩项目，两个 id。
- **Why**: macOS 的 `TMPDIR` 带尾斜杠，`mktemp -d "${TMPDIR}/…"` 原样返回 `…/T//seek-page-review.X`。夹具拿这个串 `sha256[:16]` 当项目 id 落盘；页面 `/bind` 之后服务端走 `resolveProjectPath → filepath.Abs`，双斜杠被 Clean 掉，`/state` 报的 `project_id` 是另一个串的哈希。`paths.ProjectID` 的契约就是按字节哈希、不规范化（决策 c_4c6dar），所以差一个斜杠就是两个项目。
- **Fix**: `mktemp` 之后立刻 `TMP="$(cd "$TMP" && pwd)"` 规范化一次（c_zib76h）。`cd && pwd` 只清多余分隔符、不解析符号链接，与 `filepath.Abs` 的 Clean 同口径。
- **Lesson**: 凡是「两端各算一遍同一根路径的哈希」，路径串必须逐字节相同——在产生路径的那一处规范化，别指望下游。这里不能换成 `pwd -P` / `EvalSymlinks`：服务端不解符号链接，解了反而又对不上（`/var` → `/private/var`）。原注释写的「mktemp 返回物理路径」是错的。
- **Refs**: `scripts/web-page-review.sh`（`TMP=` 规范化处）、`internal/paths/paths.go`（`ProjectID` 契约注释）、`internal/controlplane/router.go`（`resolveProjectPath`）
