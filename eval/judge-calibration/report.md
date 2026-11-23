# Judge calibration report

- ran_at: 2026-11-23T02:00:34Z
- fixtures: `eval/judge-calibration/fixtures.jsonl` (34 rows: 27 real / 7 constructed)
- judge model: `kimi-k3.1-pro` on the `opencode` lane
- **agreement: 0.88** (30/34)
- **false-pass (label fail → verdict pass): 0** — the danger direction
- false-fail (label pass → verdict fail): 0
- label pass → doubt: 1; label fail → doubt: 2
- label doubt → pass/fail (overreach): 1
- **gate eligibility (agreement ≥ 0.85 AND false-pass = 0): yes**

## Confusion (label → verdict)

```
doubt -> doubt: 1
doubt -> fail: 1
fail -> doubt: 2
fail -> fail: 7
pass -> doubt: 1
pass -> pass: 22
```

## Disagreements

```
[9] code-review-effort-max (real) label=fail verdict=doubt: judge output had no {"pass": ...} object
[21] mcp-semble-routing-a1-nl (real) label=pass verdict=doubt: judge output had no {"pass": ...} object
[25] tool-args-hallucination (real) label=doubt verdict=fail: 答案仍是未完成的过程记录，结尾还表示要继续读取用例，并未交付完整的 PRD—代码交叉验证结论。虽然提到 README 状态不一致、v8 行号漂移等线索，但缺少可复核的文件/行号或代码摘录，也没有逐项核实结果、明确的整体对齐判断及剩余问题清单，因此不满足交付具体交叉验证发现的要求。文本并未在句中截断，所以不属于 doubt。
[29] read-eof-blind (constructed) label=fail verdict=doubt: judge output had no {"pass": ...} object
```
