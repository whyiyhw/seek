### SSE 测试里 t.Cleanup 关响应体 + defer 关 server：断言一红就死等在 srv.Close
- **Saw**: `/events?topic=hooks` 测试一条 `waitSubs` 断言失败后不停在失败点——30s 超时 dump 显示挂在 `httptest.Server.Close()`（`blocked in Close after 5 seconds, waiting for connections`）。
- **Why**: `openEventsRaw` 把 `resp.Body.Close()` 注册成 `t.Cleanup`，`defer srv.Close()` 是 defer——defer 先于 Cleanup 跑；断言失败走 `Goexit` 时 SSE 连接还开着（其关闭排在 server 后面），`srv.Close()` 等所有活跃连接结束就永远等。绿时函数自然结束、LIFO defer 逐个跑完，只在失败路径现形。
- **Fix**: query 补 `&conv=` 消除该失败之外，接受既有形状：本仓 SSE 测试把 `defer resp.Body.Close()` 显式写在 `defer srv.Close()` 之前（LIFO 先关连接），helper 的 `t.Cleanup(resp.Body.Close)` 只作兜底。
- **Lesson**: defer 与 t.Cleanup 的顺序（defer 全部先跑）只在失败路径咬人——「开长连接+关 server」的测试，连接关闭必须先于 server，否则断言失败=挂死而非红。
- **Refs**: `internal/controlplane/webfrontend_topic_test.go` `openEventsRaw`；`internal/controlplane/webfrontend_hookstopic_test.go`。
