### 跨车道拒绝里的 "pass provider=X" 提示必须自己走得通——否则是伪装成提示的死胡同
- **Saw**: c_dxrjjl 的 `ResolveRoute` 点名 "pass provider=opencode"，照做即拒：`knownModels[ProviderOpenCode]` 只默认一行、选择器渲染整菜单，得 `does not belong to provider "opencode"`；`deepseek` 条目只收 `deepseek-flash`，`LaneOf("deepseek-v4-pro")` 判 deepseek；前缀 id 同指封闭名单。
- **Why**: 提示按 `LaneOf` 判 lane、条目按实收 id 判——两套判据；网关车道表该照菜单拷贝。
- **Fix**: c_tnzudu：条目改 `menuIDs` 单源；`deepseek` 条目补齐该 id；文案先问 `laneListsModel(lane, model)` 才给 `pass provider=`；`TestResolveRoute_NoDeadEndHints` 要求提示 pair 必照办。
- **Lesson**: 「照做应成功」的提示须用同一份事实验证并钉成测试：含可执行动作即断言 follow-up 不被拒。
- **Refs**: `internal/subagent/routing.go`（`knownModels` / `laneListsModel`）、`internal/subagent/routing_test.go`（`TestResolveRoute_NoDeadEndHints` / `TestResolveRoute_MenusAreRoutable`）、`internal/modelcap/lane.go`（`LaneOf` / `Serves`）、`internal/pricing/pricing.go`（`OpenCodeGoModels` / `OpenDesignModels` / `ModelV4Pro`）、`docs/guide-config.md` §3；同族 `docs/pitfalls/lane-guards-refusal-wording/闸的拒绝文案是给模型的指令面——写错场合等于教模型绕过.md`
