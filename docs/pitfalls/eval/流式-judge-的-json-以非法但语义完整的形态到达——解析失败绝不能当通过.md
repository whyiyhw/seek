### 流式 judge 的 JSON 以「非法但语义完整」形态到达——解析失败绝不能当通过
- **Saw**: 真 kimi-k3.1-pro 判分首跑连续 doubt：joined 文本里判决对象字符串值内插裸换行（规范非法，json.loads 拒收）；下一跑键名被改写为 `verdict`、值带尾换行（`"pass\n"`）。
- **Why**: 流式 delta 逐块拼接不保证 JSON 规范性——模型在字符串中间断行、改写键名、值里带空白都是稳定复现的形态；严格的解析器把这些全折成「无输出」。
- **Fix**: eval/tools/judge.py 双加固——`_repair_in_string_controls`（in-string 状态机转义 `\n\r\t`，token 间不动）+ `_verdict_from`（pass/verdict/result 三键、布尔或 pass/fail/yes/no 字符串、strip+lower；显式 null → doubt 保留理由）。4dd6abe2
- **Lesson**: LLM 产出的 JSON 按「语义完整但语法带伤」设计解析：先修（转义/键容错/空白归一）再判，修不动一律 doubt——**解析失败当通过是判分层最危险的假绿**（比 doubt 稍次的还有：把键名不认识当无输出丢掉一条真判决）。
- **Refs**: eval/tools/judge.py extract_verdict；同族 docs/pitfalls/eval/runner-sh-的-stdout-就是指标流——jq-裸调用把布尔线漏进流里.md（「工具输出按实际形态解析」的先例）。
