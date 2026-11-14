# glm-lane-contract

**Probes**: GLM 订阅车道（`--provider=glm`，open.bigmodel.cn paas/v4）的三条契约——2026-11-12/13 两次真 key 烟测坐实、此前只活在 pitfalls 文字里，回归即无声；本例把它们钉进 eval 套件：

1. **cache 字段上抛**：`-resume` 续跑的 `agent_end` 带顶层 `cache_hit_tokens ≥ 1`。GLM 经 OpenAI 兼容的 `prompt_tokens_details.cached_tokens` 报服务端前缀缓存；链路是三段——`openai/client.go` `streamUsage` 声明该字段 → `llm.TurnDone.CachedInputTokens` → `runTurnLLM` 把 `input-cached`/`cached` 劈进 miss/hit 桶（clamp 到 input）。任一环回归，字段归零。两次真 key 烟测在 resume 轮均实测 **10944** hit tokens（butler 事件 11-12 18:09 / 11-13 01:29，PR #2897）——resume 重发整个前缀，paas/v4 服务端缓存必然温热，这是断言的物理依据。
2. **pricing $ 非 0**：print 模式 stats footer 的 `est. cost:` 不是整零形状（`$0` 后跟空格）。历史上三环同漏导致二线 provider 输入恒计 $0（pitfall「pricing.Cost bills input ONLY via cache-hit/miss buckets」）；修复含 glm-5.3 费率卡 + `NoOffPeakDiscount`（DeepSeek 时钟不得给平价供应商打五折）。`runJSON` **不打印 footer**，所以这条证据只能由 print 模式腿取证。
3. **`--provider` 显式优先于 env 竞品**：11-12 烟测附带发现——shell 残留 `ANTHROPIC_API_KEY` 时自动探测先路由 anthropic（401），显式 `--provider=glm` 干净。本例全程在 env 里放 DEEPSEEK/ANTHROPIC 垃圾 key（`config.KeyFor` env 优先于 config，垃圾值能压过 config 里的真 key）：路由若输掉 flag 竞争，auto-detect 拿垃圾 key 去 auth，死、无 `agent_end`、无 footer——三条断言一起塌，且 print 腿正面断言 `model: glm*`。

**形态**: `runner.sh` 三腿链——fresh `-json`（session 落盘）→ `-resume <sid>` `-json`（cache 证据）→ print 模式（stderr footer 取 `model:` / `est. cost:` 证据）。共用隔离 `SEEK_HOME`（`eval/tmp/<case>/home/`，复制 `config.json` 供鉴权，gitignored，`teardown.sh` 清理）。footer 断言失败与进程退出码经 `runner_fact` 行上浮（`nonzero_exits` 通道，session-id-contract 的先例）。**key 走 `.env`**（gitignored，repo 真实 API 冒烟的既定规则）：runner source 仓库根 `.env` / 继承 env / 读 `~/.seek/config.json` `providers.glm.api_key`，三者皆无 → `runner_fact exit_code=78` 大声 FAIL，绝不空洞通过。`SEEK_EVAL_GLM_BASE_URL` 可选覆盖端点（指向本地假 paas/v4 服务器做管道 shakedown；真跑不设，走钉死的 bigmodel.cn URL）。

**指标**:
- `exact_agent_ends = 2` —— 两条 JSON 腿各一个 `agent_end`；任一进程中途死掉即 FAIL（错路由 auth 失败也落在这里）。
- `max_session_id_missing = 0` + `exact_session_id_distinct = 1` —— resume 同 id（顺带在 GLM 车道上锁 session 契约；DeepSeek 车道由 session-id-contract 锁）。
- `min_cache_hit_tokens = 1` —— 跨 `agent_end` 取 **max**（命中在 resume 轮；harness 对链式用例取首事件会读到冷跑的 0，`run.sh` 已改为 max）。0 = 缓存细节没到 `agent_end`（三环任一断）。
- `max_nonzero_exits = 0` —— 三腿退出码全 0，且 footer 两断言（model: glm*、cost 非整零）未触发 `runner_fact`。

**代码锚点**:
- `pkg/llm/provider/openai/client.go` `streamUsage.prompt_tokens_details.cached_tokens` / `cachedTokens()`
- `pkg/llm/llm.go` `TurnDone.CachedInputTokens`
- `pkg/agent/agent.go` `runTurnLLM`（input-cached/cached → miss/hit 分桶）
- `internal/pricing/pricing.go` `ModelGLM53` 费率卡 + `NoOffPeakDiscount` + `Cost`（只读 hit/miss 桶）
- `cmd/seek/main.go` `buildProvider`（`--provider` flag 优先序）+ `runPrint` stats footer（`model:` / `est. cost:` 行；`runJSON` 无 footer）
- `internal/config/config.go` `KeyFor`（env 优先于 config）

**怎么真跑**（主会话补跑，not-in-lane）：仓库根 `.env` 写 `GLM_API_KEY=...`（或 config.json），`eval/run.sh glm-lane-contract ./seek`。沙盒无 key 时用例会以 exit_code=78 的 runner_fact 大声 FAIL——这是设计行为，不是卡点。
