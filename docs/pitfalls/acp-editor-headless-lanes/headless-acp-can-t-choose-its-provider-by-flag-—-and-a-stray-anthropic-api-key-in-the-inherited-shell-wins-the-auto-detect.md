### Headless ACP can't choose its provider by flag — and a stray ANTHROPIC_API_KEY in the inherited shell wins the auto-detect
- **Saw**: 2026-11-13 paseo 无头冒烟：`seek acp --provider=deepseek` 的 flag 被无视；daemon 继承 shell 残留 `ANTHROPIC_API_KEY`（DeepSeek key 只在交互 shell），自动探测静默选 Anthropic，首轮跑 claude-sonnet-4。
- **Why**: ①`seek acp` 以 `os.Args[1]=="acp"` 判定车道，`flag.Parse` 在第一个位置参数处停止，ACP 车道零 flag 消费。②`buildProvider` 顺序：`--provider` → DeepSeek → Anthropic → OpenAI → Gemini → GLM → `cfg.DefaultProvider`；flag 恒空落到下一家，残留 key 上位，无告警。
- **Fix**: 无头 provider 只能走 env/config：paseo provider 块 env 带 `DEEPSEEK_API_KEY`（或 `~/.seek/config.json`，`config.KeyFor` 可读）。c_vv2mde 仅记录；2026-11-18（c_6thhqc）`SEEK_ACP_PROVIDER`/`SEEK_ACP_MODEL` env 钉定（`internal/acp/headless.go`）+ `session/set_config_option` 换车道。
- **Lesson**: 无 flag 车道的启动姿态（provider/权限/模型）只能经 env 或 config；继承 env 不干净，冒烟验「实际选了谁」不验「进程活着」。
- **Refs**: `cmd/seek/main.go`（`flag.Parse` ≈782、`acpLane` ≈933、`buildProvider` ≈2763）, `internal/config/config.go:KeyFor`, `internal/acp/headless.go`, `docs/prd/archive/feature-paseo-provider.md` §3.3, `docs/guide-zed.md` §5.1
