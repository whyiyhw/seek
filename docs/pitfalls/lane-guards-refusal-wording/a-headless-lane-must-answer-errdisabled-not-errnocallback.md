### 无头车道要给模型 ErrDisabled（可执行指令），不是 ErrNoCallback（"程序错误"）
- **Saw**: 2026-11-23 paseo 驱动的 `seek acp` 会话，模型调 `ask_user` 得 `askuser: no callback registered (programming error)`，当用法错误重试；同一文案在 `-p`、`seek -rpc`、`goal`、`autopilot` 全成立（坑 #1133 / FC-1 记过 print 半边）。
- **Why**: `askPolicy := askuser.New(askuser.ModeAsk)` 在分支前构造、全车道共用，只有 TUI 在 `tui.Run` 前 `SetAskFn`；`acpOptions` 无 askPolicy 字段——两种情形落进同一分支，报的是最少见的原因。
- **Fix**: 默认反转：出生即 `askuser.ModeDisabled`，TUI 入口显式 `SetMode(askuser.ModeAsk)`；无头车道拿 `ErrDisabled`（"interactive question not available in this mode — rephrase as a plain-text question in your reply"），模型据此把问题改写进正文。真二进制测试跑 print 与 ACP 车道，断言含 rephrase 文案且不含 `no callback registered`（改回 ModeAsk 即红）。
- **Lesson**: 两个错误值受众不同时，默认站在多数车道一侧；给模型的错误必须是可执行指令，「程序错误」只留给开发日志。
- **Refs**: `cmd/seek/main.go`（`askPolicy` 构造点 + TUI 接线块）、`internal/askuser/askuser.go`（`ErrDisabled` / `ErrNoCallback`）、`cmd/seek/askuser_lane_test.go`、`docs/guide-zed.md` §5.3、`docs/pitfalls-triage.md` FC-1
