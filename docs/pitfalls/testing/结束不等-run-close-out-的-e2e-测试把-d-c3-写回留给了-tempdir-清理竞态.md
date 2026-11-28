### 结束不等 run close-out 的 e2e 测试把 D-C3 写回留给了 TempDir 清理竞态
- **Saw**: TestWeb_ApprovalRunEndWithdrawsCard 隔离跑与 -count=3 全绿，全套 `./internal/controlplane/` 负载下 4/5 红，恒为 `TempDir RemoveAll cleanup: unlinkat …/001: directory not empty`——`/001`（newTestWeb 首个 TempDir）清理时仍被写。
- **Why**: `newStreamE2E` 的泵 `go r.Handle(ctx, m)` 无人 join；`cancelled` 帧后它仍欠 D-C3 写回 `store.Put` 与 reply close-out，测试不等即返回，写回与 `t.TempDir` RemoveAll 并发。go1.27 `os.RemoveAll` 在 [Readdirnames → removedirat] 窗口落入新文件即报；负载把写手拖过 ~150-200µs 尾部，TestWeb_UsageCarriesLaneAndWindow 只等到 usage 帧亦然。
- **Fix**: 两用例末尾各加 `wantFrame(t, tap, "", "conv":"web:default", "session_id":…)` 等 close-out（handlePrompt 末步，观测到即 happens-before 于清理）。
- **Lesson**: e2e 收到最后业务帧 ≠ run 结束：凡 newStreamE2E（真实 Router+Executor）必须等 close-out 帧；负载态红先量 cleanup 起点欠什么，再怀疑代码。
- **Refs**: internal/controlplane/webfrontend_stream_test.go、router.go `handlePrompt`、bindings.go `saveLocked`（/001 写手）、webfrontend_panel_e2e_test.go:87；姊妹 `t-cleanup-chdir-after-t-tempdir-breaks-windows-tempdir-removal.md`。
