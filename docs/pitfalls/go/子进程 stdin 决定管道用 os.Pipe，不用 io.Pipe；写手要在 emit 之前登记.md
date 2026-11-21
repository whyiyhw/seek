### 子进程 stdin 决定管道用 os.Pipe，不用 io.Pipe；写手要在 emit 之前登记
- **Saw**: 把审批决定从平面写回 `seek -p` 子进程 stdin 时，两个坑叠在一起：①`io.Pipe` 的 Write 会阻塞到读者读走——平面 RPC handler 写一行决定就可能挂住一个 HTTP 请求直到子进程调度；②若在 `emit(ev)` 之后才登记 req_id 的 delivery 闭包，浏览器可能在「卡片已弹出、写手还没登记」的窗口里点按钮，决定落空、子进程一直等。
- **Why**: os/exec 对 `*os.File` 直接传 fd（不启 copy goroutine），内核缓冲吸收写入；`io.Pipe` 是同步无缓冲的，语义上等于 goroutine 手递手。顺序问题则是「卡片的可见性」与「决定的可达性」耦合：hub 一广播卡，点击就可能发生。
- **Fix**: `attempt` 里 `os.Pipe()`，读端给 `cmd.Stdin`、写端由 `writeDecision` 在 decMu 下写；attempt 结束（成功/失败/超时/取消）`closeApprovalPipe` 关闭两端，于是迟到的决定变成 no-op 而不是挂住或 double-close。路由包一层 observer：先 `router.Route(id, write)` 再 `emit(ev)`，并在 attempt 的 defer 里 `router.Cancel()` 撤掉没人答的卡。
- **Lesson**: 「谁先可见」决定「谁先登记」——把外部可触发的入口（卡片广播、事件 emit）放在资源登记之后。管道选型看谁写：别的 goroutine 写子进程 stdin ⇒ os.Pipe。
- **Refs**: `internal/controlplane/executor.go` `attempt`（ApprovalRouter 段）；`internal/controlplane/webfrontend_approval.go` `runApprovals`；卡 `c_n37tnr` 子进程半边。

## Web frontend / asset plane
