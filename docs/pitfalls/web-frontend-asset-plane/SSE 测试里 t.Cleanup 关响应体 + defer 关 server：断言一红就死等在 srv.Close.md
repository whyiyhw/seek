### SSE 测试里 t.Cleanup 关响应体 + defer 关 server：断言一红就死等在 srv.Close
- **Saw**: 新增的 `/events?topic=hooks` 测试里一条 `waitSubs` 断言失败后，测试不在失败点停——30s 超时 dump 显示挂在 `httptest.Server.Close()`（`blocked in Close after 5 seconds, waiting for connections`），一台 handler goroutine 永远在 select 里。
- **Why**: `openEventsRaw` 把 `resp.Body.Close()` 注册成 `t.Cleanup`，而 `defer srv.Close()` 是 defer——**defer 先于 Cleanup 跑**。断言失败走 `Goexit` 时，SSE 连接还开着（它的关闭排在 server 后面），`srv.Close()` 等所有活跃连接结束就永远等下去。测试绿时没人碰这个顺序（函数自然结束前 LIFO defer 逐个跑完），所以只在失败路径现形。
- **Fix**: 让失败不发生（query 补 `&conv=` 参数）之外，结构上接受既有形状：本仓 SSE 测试把 `defer resp.Body.Close()` 显式写在 `defer srv.Close()` 之前（LIFO 保证先关连接），helper 里 `t.Cleanup(resp.Body.Close)` 只作兜底。新写 SSE 测试照抄这个顺序，别只依赖 Cleanup。
- **Lesson**: `defer` 与 `t.Cleanup` 的执行顺序（defer 全部先跑）在失败路径上才咬人——任何「开着长连接 + 结束时关 server」的测试，连接的关闭必须能先于 server 的关闭执行，否则断言失败=测试挂死而非测试红。
- **Refs**: `internal/controlplane/webfrontend_topic_test.go` `openEventsRaw`；`internal/controlplane/webfrontend_hookstopic_test.go`。
