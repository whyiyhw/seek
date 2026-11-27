### 宿主只传模型名时 lane 落到 auto-detect —— 网关 wire id 被送进官方 DeepSeek 车道，上游 400 报支持名单
- **Saw**: Paseo（`opendesign:deepseek-v4.1-flash-eco`）两次报 `deepseek api error: invalid_request_error: The supported API model names are deepseek-flash, deepseek-v4-pro, but you passed deepseek-v4.1-flash-eco`（2026-11-25）；模型没下线，lane 走错。
- **Why**: 宿主回传不带 provider；`eco` 是 OpenDesign 的 wire id，`buildProvider` 的 auto-detect 有 deepseek key 即选 deepseek——`LaneOf` 能解出 `opendesign` 却无校验。
- **Fix**: c_dxrjjl：`acpStartupLaneGate`（pin 后、`buildProvider` 前）：`LaneOf` 给出第二梯队 lane 而生效 provider 不符即启动报错 `set SEEK_ACP_PROVIDER=opendesign`；auto-detect 抽成 `resolveProviderName` 单源；新增 `Serves(lane, model)`，`LaneOf` 为空放行。续（c_tnzudu）：菜单单源 `pricing.OpenCodeGoModels`/`OpenDesignModels`，`deepseek` 补 `deepseek-v4-pro`，`pass provider=` 仅在收录时给。
- **Lesson**: 模型 id 单独存在时先过 `LaneOf` × 生效 provider 一致闸——「谁拥有此 id」vs「能否发」由 `LaneOf`/`Serves` 分别作答。
- **Refs**: `cmd/seek/acp_lane_gate.go`、`cmd/seek/main.go`、`internal/modelcap/lane.go`、`internal/subagent/routing.go`、`cmd/seek/subagent_glue.go`、`internal/tools/agent/agent.go`；同族 `second-subscription-lane-sharing-a-wire-id-capability-tables-have-no-provenance.md`
