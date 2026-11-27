### 多 GOOS 仓里 `deadcode` 只按当前 GOOS 扫描 — build-tag 下 live 的符号会被报成死码
- **Saw**: 减法卡 c_bufwel 第 6 刀（2026-11-24）`go tool deadcode` 把 `routines.noopNotifier` 判为候选；实为 linux（headless）与 windows（BurntToast 未落地）`DefaultNotifier` 的 live 实现，darwin 看不到。
- **Why**: `deadcode` 一次只覆盖一个 `GOOS/GOARCH`；`//go:build linux`/`windows`/`!darwin && !linux && !windows` 变体与引用不在 darwin 包，符号无入边即报 unreachable。`tools/bash` 的 `bash_{unix,windows}.go`/`proc_{linux,other_unix}.go` 同为候选。
- **Fix**: 不删——保留为三处 build-tag 引用（notify_linux.go:26、notify_windows.go:20、notify_other.go:11）共用降级；`GOOS=linux`/`windows` `go build ./...` 全过，`grep -rn "noopNotifier" --include="*.go" internal/` 三处非测试引用分属三变体。
- **Lesson**: 删前跨平台核对 build-tag 引用：候选在按 `//go:build` 分派的同名多文件包，先 grep 全部变体再各 GOOS build；单平台 deadcode 的「生产不可达」是伪信号。跨平台复扫在 host 构建 deadcode、传 `GOOS=x` 运行（`GOOS=x go tool deadcode` 连工具也按目标平台编译，exec format error）。
- **Refs**: `internal/routines/notify{,_linux,_windows,_other}.go`、`docs/notes-slim-candidates-2026-11.md` §2（`sandbox.Wrap` 同族+跨 GOOS 复扫命令）、card c_bufwel/c_fpnlir
