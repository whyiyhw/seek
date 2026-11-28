### 抽取式判分器的否定答案不能回显金标串 —— 否则「没找到」读成「找到了」
- **Saw**: compact-ab 负变体（丢掉三个金标事实）本该让 `arm_b_negative_loses_facts` 变绿，首跑却红：detail 写 `text='NO-FACTS-AVAILABLE (missing: internal/csvexport/export.go, report.csv, 45s)'`——判分器在答案文本找这三个串，否定答案自己列出了它们。
- **Why**: 端点 fallback 把缺失 fact 串回显进答案（让人看懂缺什么）；判分是纯 `in text` 抽取匹配（假 key 车道零 judge 的根基），「没答出」恰好含「答出了」的全部特征。
- **Fix**: 否定答案只报计数（`NO-FACTS-AVAILABLE (0/3 facts in history)`），不回显金标串；约束写进端点文件头（"the negative answer must NOT echo the fact strings"）——隐式契约，改一端不改另一端断言即失效。
- **Lesson**: 判分器用「答案里有没有 X」判定时，失败输出须与成功输出在特征空间正交——人可读的 "missing: X, Y" 恰含判分器要找的 X、Y。检查法：成功答案换成失败答案，判分输出必须翻转。
- **Refs**: `eval/tools/compact-ab-endpoint.py:answer_for`、`eval/cases/compact-ab/runner.sh`（`FACTS`）、`scripts/eval-compact-ab-test.sh`
