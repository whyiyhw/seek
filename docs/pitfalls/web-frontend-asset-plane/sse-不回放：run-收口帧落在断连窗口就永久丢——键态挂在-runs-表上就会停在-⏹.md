### SSE 不回放：run 收口帧落在断连窗口就永久丢——键态挂在 runs 表上就会停在 ⏹
- **Saw**: c_5m3fm6（→ 卡 c_6mbjfq）：生成中断开连接、run 在断连期间跑完，重连后停止键停在「⏹ 停止」；发 /cancel 只换回 run-less 的「当前没有正在运行的任务。」，键不复位。
- **Why**: 服务端只广播无回放（`handleEvents` replay nothing），收口帧只在当时订阅者缓冲区里；`runs` 表靠收口帧删条目，帧丢则表永远非空，`renderSend` busy 判据是 `runs.size > 0`，run-less 应答原来不碰表。
- **Fix**: 复用 /cancel 武装位当判据：发过 /cancel 回来的却是 run-less 应答＝服务端无 run 可中断、只是收口丢了；`closePhantom` 按 c_5m3fm6 保留式收口（半截+「—— 已中断 ——」+删表项）。否决重连即清表（EventSource 瞬时抖动也自动重连，盲清截断真在跑气泡）与按 run 龄清理（新增机制）。
- **Lesson**: 按播报增量维护的客户端状态必须自带「增量永不来」的收敛信号；判据要落在只有该路径才有的应答上（run-less 应答无 `if (stopping)` 守卫会误复位真在跑状态）。
- **Refs**: `internal/controlplane/webfrontend.html` `closePhantom` + `es.onmessage` 的 run-less 分支；`internal/controlplane/webfrontend_test.go` `TestWeb_PagePhantomRunResetsStopKey`；`scripts/web-stop-headless.mjs` 景 3。
