### Two `time.Now()` calls in the same nanosecond make Equal-based timestamp assertions flaky
- **Saw**: `TestRecall_HitReturnsEntryAndBumpsRecall` intermittently failed with `LastRecalledAt should advance past CreatedAt after Recall`; more often on fast CI runners with coarse clock resolution.
- **Why**: `Project.Add` and `memorytool.Execute → TouchRecall` each call `time.Now().UTC()`; when both land in the same nanosecond `LastRecalledAt.Equal(CreatedAt)` is true though Recall did update the timestamp — the bug is the test's assumption that `time.Now()` strictly advances, not production.
- **Fix**: insert `time.Sleep(time.Millisecond)` between `p.Add` and `tool.Execute`; 50/50 race-mode runs pass after.
- **Lesson**: `t1, t2 := time.Now(), time.Now(); t1 == t2` is allowed by the Go spec (same monotonic tick); "later strictly greater" assertions need a sleep or fake clock — don't depend on real-clock progress in fast tests.
- **Refs**: `internal/tools/memorytool/memorytool_test.go:TestRecall_HitReturnsEntryAndBumpsRecall`, `internal/memory/project.go:Add` (line 266 `time.Now`), `internal/tools/memorytool/memorytool.go:92` (line 92 `time.Now`)
