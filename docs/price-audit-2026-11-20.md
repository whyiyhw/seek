# 价格台账巡检报告 · 2026-11-20

- 触发：人工（manual）——c_ldu44u 巡检脚本首跑；不进 cron，理由见 scripts/price-audit.sh 头注
- 只读声明：源均为 public GET，本次运行唯一写入 = 本报告；台账/代码零改动
- 改卡流程：人读「源抓取证据」比对页内数字 → 确认后手改价卡并 bump 台账 VerifiedOn（脚本永不改台账）

## 两检查结果

```
# 巡检完成：发现 0 过期 / 0 孤儿（阈值 90 天；发现=报告项，不拦构建）
```

## 台账（25 行）

| id | verified-on | 币种 | source |
|---|---|---|---|
| beam-free | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| deepseek-flash | 2026-11-15 | USD | https://api-docs.deepseek.com/quick_start/pricing |
| deepseek-v4-pro | 2026-11-15 | USD | https://api-docs.deepseek.com/quick_start/pricing |
| deepseek-v4.1-flash | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| glm-5.2 | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| glm-5.3-flash | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| glm-5.3 | 2026-11-12 | CNY | https://docs.bigmodel.cn/cn/guide/start/pricing |
| hy3 | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| hy4-flash-free | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| hy4-preview | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| hy4 | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| iceman-contributor-free | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| kimi-k2.7-code | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| kimi-k3.1-pro | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| kimi-k3 | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| longcat-2.0 | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| longcat-2.5-free | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| mimo-v2.6-flash | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| mimo-v2.6-pro | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| minimax-m3.1-flash | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| step-5-preview | 2026-11-18 | USD | https://opencode.ai/inference/go/v1/models |
| tool:asr | 2026-11-16 | USD | https://api-docs.deepseek.com/quick_start/pricing |
| tool:parse | 2026-11-18 | USD | https://api-docs.deepseek.com/quick_start/pricing |
| tool:search | 2026-11-18 | USD | https://api-docs.deepseek.com/quick_start/pricing |
| tool:tts | 2026-11-16 | USD | https://api-docs.deepseek.com/quick_start/pricing |

## 源抓取证据（3 个去重源）

| http | bytes | sha256 | url |
|---|---|---|---|
| 200 | 29502 | `38d9c0ff29a72b17651aaa742a610b4eee4b34f3df803db811d3fa17388ec41e` | https://api-docs.deepseek.com/quick_start/pricing |
| 200 | 221554 | `268f043f2f6e524e052f60f8191d62114faee19a2921b465bd386956b61f44df` | https://docs.bigmodel.cn/cn/guide/start/pricing |
| 200 | 42301 | `4f75dd0b0921af0b04ed585c122e0f990173ada0c44d677f1f5453ce39442c58` | https://opencode.ai/inference/go/v1/models |

## 人工比对区（巡检官员填：页内数字 vs 台账对应卡）

比对执行：c_ldu44u 巡检首跑（脚本骨架 + 人工读抓取证据，UTC 2026-11-20）。三源均为 SSR/JSON，curl 全可读，无需浏览器。

### DeepSeek EN 价格页（peak 档，USD/1M）

| 台账行 | 页内现价 | 价卡 | 结论 |
|---|---|---|---|
| deepseek-flash | miss $0.30 · hit $0.006 · out $1.20 | 同 | ✓ 确认 |
| deepseek-v4-pro | miss $1.32 · hit $0.044 · out $3.96 | 同 | ✓ 确认 |
| tool:search | $0.75 / 1K 成功请求 | 同 | ✓ 确认 |
| tool:asr | $0.116 / 时（标价列） | 同 | ✓ 确认 |
| tool:tts | $0.45 / 万计费字符（标价列） | 同 | ✓ 确认 |
| tool:parse | $0.225 / 千页 | 同 | ✓ 确认 |

附注（页内政策声明，不改卡）：峰窗 01:00–04:00 / 06:00–10:00 UTC 周一至五、剔除中国法定节假日——与 `CurrentTier`/`NextTransition` 的周内逻辑一致；legacy id `deepseek-v4-flash`/`-flash-vision-exp` 仍被接受、路由至 V4.1-Flash 按 Flash 价计——与 unknown-model fallback 设计一致；**2026-11-18 12:00（北京时间）起模型调用触发的服务端 search/fetch 工具不再计费**（独立 Search/Fetch API 费率不变）——seek 的 `SearchCost` 计的是独立 API，口径不受影响，暂无需改卡。

### bigmodel GLM 页（CNY/1M，native 币种）

| 台账行 | 页内现价 | 价卡（@7.0 转 USD） | 结论 |
|---|---|---|---|
| glm-5.3 | 输入 ¥8 · 缓存命中 ¥2 · 输出 ¥28（1M 上下文单档；缓存存储限时免费） | ¥8/¥2/¥28 | ✓ 确认 |

### opencode Go 目录（GET /v1/models，USD/1M cost.text 峰值）

18 个池内台账行逐一比对 cost 块：16 行无乘数、listed==peak 直接一致；`deepseek-v4.1-flash` 与 `deepseek-v4-pro` 目录报基价 ×2 周内峰窗乘数（factor 字段）= $0.30/$0.006/$1.20 与 $1.32/$0.044/$3.96，与卡一致（卡的「保峰值」口径）。

**漂移发现（本卡只报告，不改卡）**：

- **`glm-5.2` 已不在目录**（台账 2026-11-18 尚可见，2026-11-20 GET 已无此 id；池子周级翻新是既知节奏）。它的菜单行/价卡/预算行仍在。是否从 `OpenCodeGoModels`/`standardRates`/`contextLimits` 摘除，留主会话按 docs 页面复核后决策（池内下架 ≠ 网关立即拒答，与「退役 id 静默应答」同族，不急但该记）。
- 目录另有 12 个台账外 id（claude-haiku-5-5、gemini-4-argon、gpt-5.6-luna、gpt-6-luna、grok-4.7、minimax-m3、muse-spark-1.{2,3}-contributor、qwen3.7-plus、qwen3.8-{flash,max}、qwen4-max）——全部命中 `OpenCodeGoModels` 注释的「deliberately absent」清单（非 Chat-Completions 线），无新增未知线，不构成孤儿。

### 处置

- 24 行台账 VerifiedOn bump 至 2026-11-20（上表确认者）；`glm-5.2` 保持 2026-11-18 留察。
- 本报告「台账」快照是巡检时点（bump 前）状态；bump 见本卡 commit 的 ledger.go diff。
