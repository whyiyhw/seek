# compact-ab

**Probes**: `/compact` 到底保不保住任务所需的事实——同一 follow-up prompt、
同一模型、同一 cwd，唯一变量是送给模型的历史：**arm A 全历史** vs
**arm B `/compact` 留下的 summary pair**。

**为什么不用运行时 compact**: `/compact` 只在交互 TUI 内可执行，
auto-compact 也仅 TUI 车道（[`docs/guide-budget.md`](../../../docs/guide-budget.md) §2.5），
`seek -p` 没有 compact 命令。所以本用例**构造会话文件**做对照，而不是
"先压再问"：两臂的差异被钉死在磁盘上，可复算、无随机性。

**臂 B 的形状取自代码，不是我编的**：
`handleCompactDone`（`internal/tui/update_agent.go`）把子会话 reset 成两条消息——

```
user      "Here is a summary of our earlier conversation. Continue from this context:\n\n<summary>"
assistant "Understood — I have the context. Ready to continue."
```

`eval/tools/compact-ab-fixture.py` 逐字复现这两条，`--summary good|bad`
是它唯一的自由度。

**为什么不用 judge.py**: 假 key 车道上"模型"必然是本仓自己脚本化的端点
（[`../README.md`](../README.md) § Isolation 的无人值守纪律），让 judge 去评
自己写的罐头文案，测的是端点而不是压缩。本用例改成**抽取式脚本端点**
（`eval/tools/compact-ab-endpoint.py`）：请求体里三个金标事实
（`internal/csvexport/export.go` / `report.csv` / `45s`）齐了才答，
缺一个就答 `NO-FACTS-AVAILABLE`。于是"答出了事实"只能来自历史，
而事实这三个串只存在于 arm A 的历史与（good 变体的）arm B 摘要里，
从不出现在 prompt 里。真 key 车道可另加 `judge` 块（rubric 文本已编码为该端点的 FACTS）。

**判分（全部机械，`runner_fact` 行 → `runner_assert_failures`）**:

| 断言 | good 变体 | bad 变体 |
|---|---|---|
| `arm_a_keeps_facts` | 过（全历史必答出三事实） | 过（同上——A 红说明 harness 坏了，不是压缩丢了东西） |
| `arm_b_keeps_facts` | 过（摘要带事实，答得出） | — |
| `arm_b_negative_loses_facts` | — | 过（摘要不带事实 ⇒ 答 `NO-FACTS-AVAILABLE`） |
| `arm_b_is_summary_pair` | 过（种子文件就是摘要对，不是历史副本） | 过 |

**负验证**（`scripts/eval-compact-ab-test.sh` 钉死）：`COMPACT_AB_SUMMARY=bad`
跑同一条管线，`arm_b_negative_loses_facts` 必须**绿**而 arm A 仍绿——
这证明断言能分辨"摘要丢了东西"，不是恒绿。反向做一次：把端点改成
"永远答出事实"，`arm_b_negative_loses_facts` 立刻转红。

**"arms 不必可区分"是刻意的**：good 变体两臂答出的文本逐字相同——
抽取式端点的正确行为。真正区分两臂的是**历史**，由
`arm_b_is_summary_pair` 结构断言钉住（读的是**跑之前**的种子文件快照，
因为 seek 跑一轮会重写会话文件、把 system prompt 重新落盘）。

**形态**: `runner.sh` 起 `compact-ab-endpoint.py`（随机空端口，`--rounds`
并发不撞）、`compact-ab-fixture.py` 种两臂、同一 prompt 分别
`-resume arm-a-full` / `-resume arm-b-summary` 跑真二进制，两段 JSONL
拼流交给 `eval/run.sh` 的 metric 提取器；`teardown.sh` 清 `eval/tmp/compact-ab`。
隔离走 `SEEK_HOME=eval/tmp/compact-ab/home`（不碰真实 `~/.seek`），
`-provider compatible -base-url <fixture>` + 假 key，零 API 费。

**record_only**: 只记录不闸门——真 key 车道与 E2 标定之前没有资格拦截
（[`docs/prd/feature-eval-v3.md`](../../../docs/prd/feature-eval-v3.md) §2.1）。

**离线孪生**: `scripts/transcript-stats` 的 compact 链段
（`chains.compression_mean` / `chains.recall` / `chains.child_head_rate`）
是同一批问题在真实历史上的连续量版本——本用例回答"特定构造下丢没丢"，
分析器回答"真实会话里平均丢多少"。
