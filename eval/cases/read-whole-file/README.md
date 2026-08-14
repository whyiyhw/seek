# read-whole-file

**Probes**: 整文件理解的成本主线（H1）——轮次 × 输出 token 是主要开销维度，而 50 行 cap 把它们乘到最大。

- fixture `testdata/server.go`：400 行，6 个函数签名依次出现在第 6/22/38/54/70/86 行（已程序化断言唯一且有序）。
- 基线：400 行 ÷ 50 = 8 次分页 + 可能的 EOF 探测；改进（I2 阈值 200 行内整体读取）：1 次。
- 金标（离线）：`handleAuth`、`parseConfig`、`retryOnce`、`mergeResults`、`validateInput`、`writeReport` 无序命中 ≥5/6（避免顺序惩罚淹没 read 行为信号）。
- 成本核算（离线）：用结果行的 prompt_tokens / completion_tokens / cache_hit_tokens 按 `docs/test-plan-read-tool.md` §3.3 公式计算。
- 关联：`docs/test-plan-read-tool.md` §6 Case C。
