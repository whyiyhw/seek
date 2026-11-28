### 批量编辑的机械核对脚本：基准钉不可变提交，行内 span 按等长反引号配对
- **Saw**: 瘦身卡（c_snscbi，324 条 pitfalls 批量删冗余）的一次性核对脚本两种假红各来一次：①以 `main` 为基准，跑着跑着别的会话把一条新条目合进 main，脚本把「main 有、工作树没有」的**新增**文件报成「文件已不存在」；②文中 ` ```json\n[...]\n``` ` 让「取两个反引号之间」的配对整体错位，凭空造出 4 个伪 token（`and`、`json` …），连 main 原文自己都过不了。
- **Why**: main 是活分支，任何「对 main 比对」的长跑核对都会中途换基准；行内代码 span 的定界是**等长**反引号段（CommonMark），三连围栏的奇偶会把后续配对串错。
- **Fix**: 基准钉本卡开工的 base 提交（`git show <base>:<path>`），合并 main 后仍以 base 比对；span 切分按等长配对（`code_spans`），围栏不计 token。
- **Lesson**: 「事实没丢」的证明要先声明两件事——基准是哪个不可变提交、token 怎么切分；写 `main` 当基准等于让证明随别人的提交漂移。
- **Refs**: `docs/evidence/c_snscbi/verify-facts.py`（`code_spans`、`BASE` 参数）、`audit-extra.py`、卡 c_snscbi
