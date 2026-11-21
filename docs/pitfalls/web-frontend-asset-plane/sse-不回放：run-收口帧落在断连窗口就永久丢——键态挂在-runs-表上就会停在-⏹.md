### SSE 不回放：run 收口帧落在断连窗口就永久丢——键态挂在 runs 表上就会停在 ⏹
- **Saw**: c_5m3fm6 验证的非阻断观察（→ 卡 c_6mbjfq）：生成中断开连接、run 在断连期间跑完，重连后停止键一直停在「⏹ 停止」；点它发出的 /cancel 只换回一句 run-less 的「当前没有正在运行的任务。」，键仍然不复位。
- **Why**: 服务端每条连接只广播、无回放（`handleEvents` 明示 replay nothing），run 的收口帧（带 run id 的 reply）只存在于那一刻的订阅者缓冲区里；页面的 `runs` 表靠这份收口帧删条目，帧丢了表就永远非空，而 `renderSend` 的 busy 判据正是 `runs.size > 0`。run-less 应答走的是纯消息分支，原来不碰表。
- **Fix**: 复用既有的 /cancel 武装位当判据——「本页为某 run 发过 /cancel、回来的却是 run-less 应答」＝服务端没有 run 可中断了，那个 run 早结束、只是收口丢了；`closePhantom` 按 c_5m3fm6 的保留式收口（半截保留 + 「—— 已中断 ——」标注 + 删表项），键态随表自动复位。**否决**「重连即清表」：EventSource 对任何瞬时抖动都自动重连，盲清会把真在跑的气泡截断、后续 delta 变第二个泡；也否决按 run 龄清理（要额外时间戳 + 扫描，属新增常驻机制）。
- **Lesson**: 客户端按服务端播报增量维护的状态，必须自带一个「这条增量永远不会来」的收敛信号；能复用已有的用户动作位（这里是 /cancel 武装）就别新造机制。判据要落在「只有这条路径才会发生的应答」上，否则会把真在跑的状态一起复位（run-less 应答不加 `if (stopping)` 守卫就会这么错）。
- **Refs**: `internal/controlplane/webfrontend.html` `closePhantom` + `es.onmessage` 的 run-less 分支；`internal/controlplane/webfrontend_test.go` `TestWeb_PagePhantomRunResetsStopKey`；`scripts/web-stop-headless.mjs` 景 3。
