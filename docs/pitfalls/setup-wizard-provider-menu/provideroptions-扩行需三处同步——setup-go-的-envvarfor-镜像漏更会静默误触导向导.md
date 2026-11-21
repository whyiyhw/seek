### providerOptions 扩行需三处同步——setup.go 的 envVarFor 镜像漏更会静默误触导向导
- **Saw**: 给 providerOptions 加 opencode/glm 行时，若不同步 setup.go 本地 envVarFor（镜像 internal/config 同名函数），env-only 用户（已 export `OPENCODE_API_KEY`/`GLM_API_KEY`）仍被 shouldTriggerWizard 判为「零 key」弹向导；向导保存完的 Tip 行也渲染成 `=... seek`（envVarFor 返回空串）。另有第三处：两处重试提示独立写死范围 "1–4"，与菜单数据无联动，加行即失真。
- **Why**: envVarFor 按 providerOptions 逐项查 env，镜像少一个 case 新 lane 的 env 凭证就查不到；范围文案与菜单行数是两份手抄数据。
- **Fix**: 卡 `c_nxirmj`：提示改按 `len(providerOptions)` 动态生成；envVarFor 镜像补 glm/opencode 两 case，`TestEnvVarFor_KnownProviders` 同步扩表锁死。
- **Lesson**: 向导扩 provider 的清单：providerOptions 行 + setup.go envVarFor case + internal/config envVarFor case（范围文案已动态化）。凡是「镜像别处枚举」的 switch，扩表当轮就补测试用例。
- **Refs**: `cmd/seek/setup.go` `providerOptions`/`envVarFor`/`shouldTriggerWizard`，`internal/config/config.go` `envVarFor`。
