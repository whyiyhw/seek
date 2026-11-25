### 抽取式判分器的否定答案不能回显金标串 —— 否则「没找到」读成「找到了」
- **Saw**: compact-ab 的负变体（摘要故意丢掉三个金标事实）本该让 `arm_b_negative_loses_facts` 变绿。第一次跑却红了，detail 写着 `text='NO-FACTS-AVAILABLE (missing: internal/csvexport/export.go, report.csv, 45s)'` —— 判分器在答案文本里找这三个串，而**否定答案本身把它们列了出来**。
- **Why**: 端点的 fallback 分支为了让人看懂「缺了什么」，把缺失的 fact 字符串回显在答案里。判分是纯 `in text` 的抽取式匹配（这是它能在假 key 车道保持机械、零 judge 的原因），于是「没答出」的答案恰好包含了「答出了」的全部特征。判分器无法区分「答案在陈述事实」与「答案在报告自己缺少事实」。
- **Fix**: 否定答案只报**计数**（`NO-FACTS-AVAILABLE (0/3 facts in history)`），不回显任何金标串。并把这个约束写进端点文件头（"the negative answer must NOT echo the fact strings"），因为它是判分器与端点之间的隐式契约——改一端不改另一端，断言就会静默失效。
- **Lesson**: 当一个判分器用「答案里有没有 X」来判定时，它的**失败输出**必须与成功输出在特征空间上正交。人读得懂的失败说明（"missing: X, Y"）往往正好含有判分器要找的 X、Y——这是「可读性」与「可判定性」的冲突，取后者，把可读性让给日志/计数。反过来检查也很简单：把成功答案换成失败答案，判分器的输出必须翻转；没翻转就是特征重叠了。
- **Refs**: `eval/tools/compact-ab-endpoint.py:answer_for`、`eval/cases/compact-ab/runner.sh`（`FACTS` 匹配）、`scripts/eval-compact-ab-test.sh`（破坏态把这条钉成回归）
