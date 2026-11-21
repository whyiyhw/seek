### 测试 swap 包级 poll var，follower goroutine 读它：栈扫描不是 join，-race 必炸
- **Saw**: `go test -race ./internal/controlplane/` 间歇红：DATA RACE — write `webfrontend_hookstopic_test.go:36`（fastHooksPoll 的 t.Cleanup 还原 `hooksTopicPoll`）vs previous read `webfrontend_hookstopic.go:93`（`hooksFollowLoop` 里 `time.NewTicker(hooksTopicPoll)`），挂在 TestWeb_TopicHooksHeadIntegrity。
- **Why**: follower goroutine 由 handler 在 subscribe 时 spawn，但它的首条读可以发生在任何时刻——HeadIntegrity 读一行就挂断，follower 可能晚到连 `waitHooksHubEmpty` 的栈扫描都错过窗口；更根本的是**轮询 `runtime.Stack` 直到函数名消失不建立任何 happens-before**（race detector 只认真正的同步原语），所以哪怕 wall-clock 上读先于还原写，detector 眼里两者无序。
- **Fix**: 读点钉进会被 join 的 goroutine：`subscribeHooksTopic`（handler goroutine、持 f.mu）读 `hooksTopicPoll` 作参数传 `hooksFollowLoop(fo, fl, poll)`，loop 不再碰包级 var。handler 被 `httptest.Server.Close`（defer）真 join，而 defer 先于 t.Cleanup（还原）——swap 与还原两侧都有了同步边。`subagentsTopicPoll`/`followLoop` 同构一并改（W5 c_xz2bey 返工 2）。
- **Lesson**: 「测试收紧包级 var」时，var 的读点必须在 spawn 处捕获、按值传进新 goroutine；让被 spawn 的 goroutine 自己去读包级 var，等于把读挂在一条没有同步边的生命期上——生产无害（没人写它），`-race` 下测试必炸。同理：等 goroutine 退出的正确姿势是 done chan / WaitGroup 这类真 join，栈扫描只是观测、不是同步。
- **Refs**: `internal/controlplane/webfrontend_hookstopic.go` subscribeHooksTopic/hooksFollowLoop；`internal/controlplane/webfrontend_topic.go` 同构对。

## Release / upgrade
