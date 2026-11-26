### 测试 swap 包级 poll var，follower goroutine 读它：栈扫描不是 join，-race 必炸
- **Saw**: `go test -race ./internal/controlplane/` 间歇红：DATA RACE — write `webfrontend_hookstopic_test.go:36`（t.Cleanup 还原 `hooksTopicPoll`）vs previous read `webfrontend_hookstopic.go:93`（`hooksFollowLoop` 里 `time.NewTicker(hooksTopicPoll)`），挂在 TestWeb_TopicHooksHeadIntegrity。
- **Why**: follower 首条读可在任何时刻（可能晚到连 `waitHooksHubEmpty` 栈扫描都错过）；轮询 `runtime.Stack` 到函数名消失不建立 happens-before，race detector 只认真同步原语。
- **Fix**: 读点钉进被 join 的 goroutine：`subscribeHooksTopic`（handler、持 f.mu）读 `hooksTopicPoll` 作参数传 `hooksFollowLoop(fo, fl, poll)`；handler 被 `httptest.Server.Close`（defer）真 join、defer 先于 t.Cleanup。`subagentsTopicPoll`/`followLoop` 同构改（W5 c_xz2bey）。
- **Lesson**: 测试收紧包级 var：读点在 spawn 处捕获、按值传入，goroutine 自读包级 var 即挂在无同步边的生命期上、`-race` 必炸；等退出用 done chan/WaitGroup，栈扫描只是观测。
- **Refs**: `internal/controlplane/webfrontend_hookstopic.go` subscribeHooksTopic/hooksFollowLoop；`internal/controlplane/webfrontend_topic.go` 同构对。
