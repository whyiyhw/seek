### SSE 不回放：run 收口帧落在断连窗口就永久丢——键态挂在 runs 表上就会停在 ⏹
- **Saw**: c_5m3fm6（→ 卡 c_6mbjfq）：生成中断连、run 在断连期间跑完，重连后键停在「⏹ 停止」；发 /cancel 只换回 run-less 的「当前没有正在运行的任务。」，键不复位。
- **Why**: 服务端只广播、无回放（`handleEvents` replay nothing），收口帧只落在当时订阅者缓冲；`runs` 表靠收口帧删条目，帧丢则表永非空，而 `renderSend` 的 busy 判据是 `runs.size > 0`。
- **Fix**: 复用 /cancel 武装位当判据：发过 /cancel 却回 run-less 应答 = 服务端无 run 可中断、只是收口丢了；`closePhantom` 按 c_5m3fm6 保留式收口（半截+「—— 已中断 ——」+删项）。否决「重连即清表」（抖动也重连，盲清截断真在跑的气泡）与按 run 龄清理。
- **Lesson**: 按播报增量维系的客户端状态必须自带「增量永不来」的收敛信号；判据须落在只有该路径才有的应答上（run-less 应答缺 `if (stopping)` 守卫会误复位真在跑状态）。
- **Refs**: `internal/controlplane/webfrontend.html` `closePhantom` + `es.onmessage` 的 run-less 分支；`internal/controlplane/webfrontend_test.go` `TestWeb_PagePhantomRunResetsStopKey`；`scripts/web-stop-headless.mjs` 景 3。
