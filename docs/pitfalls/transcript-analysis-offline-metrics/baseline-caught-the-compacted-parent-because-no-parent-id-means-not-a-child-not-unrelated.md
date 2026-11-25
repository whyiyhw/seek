### compact 链的参照系把「被压缩掉的那个会话」也算进去了，重读率被稀释
- **Saw**: 给 `scripts/transcript-stats` 加 compact 链口径（card c_5hh4uu）时，参照系定义成「同 cwd、无 `parent_id` 的会话的聚合重读率」。fixture 上算出来 25.00%，而手工数应当是 50.00%（唯一一条无 fork 的会话读 d.md 两遍）。真数据上它给出 70.25%（224 会话 / 4974 read），看上去「很合理」，所以肉眼发现不了。
- **Why**: `parent.jsonl` 没有 `parent_id`（它是父），于是被归进「没压缩的会话」这一组。可父会话正是被压缩掉的那段历史——它的 read 不属于「没压缩的会话长什么样」。分母里混进一个恰好对应链另一端的样本，参照系就被拉向链自己的形状，比值失去参照意义。这不是分母算错，是**分组口径错**：`无 parent_id` 只说明「不是子」，不说明「与链无关」。
- **Fix**: 参照系排除 `parent_id` 非空 **且** `id ∈ 链的 parent_id 集合**的会话（两条件都要）。fixture 断言从 25.00% 变成 50.00%，并把「父会话不算参照系」写进 `scripts/transcript-stats` 头注释与 `eval/fixtures/transcript-stats/README.md`，fixture 的第四条会话 `plain-no-fork` 命名即为此。
- **Lesson**: 对照组的分组条件必须写成「与实验组无关」而不是「看起来没被处理」——「没有 X 标记」是个弱条件，任何与实验组同源但对端不标记的样本都会漏进来。反过来说，当一个参照数字看起来合理而你无法用一句话解释它的分母时，去数 fixture。
- **Refs**: `scripts/transcript-stats:analyse_chains`、`eval/fixtures/transcript-stats/compact-chain/`、`eval/README.md`（`chains.baseline` 口径行）
