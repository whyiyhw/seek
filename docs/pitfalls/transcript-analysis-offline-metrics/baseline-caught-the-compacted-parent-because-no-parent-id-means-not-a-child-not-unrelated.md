### compact 链的参照系把「被压缩掉的那个会话」也算进去了，重读率被稀释
- **Saw**: compact 链（card c_5hh4uu）参照系是「同 cwd、无 `parent_id` 的会话的聚合重读率」；fixture 25.00%，手工 50.00%（唯一无 fork 会话读 d.md 两遍），真数据 70.25%（224 会话 / 4974 read）看似合理。
- **Why**: `parent.jsonl` 没有 `parent_id`（它是父），被归进「没压缩的会话」，但它正是被压缩掉的历史；`无 parent_id` 只说明「不是子」，不说明「与链无关」——分组口径错，非分母算错。
- **Fix**: 参照系排除 `parent_id` 非空**且** `id ∈ 链的 parent_id 集合`的会话（两条件都要）；fixture 断言 25.00%→50.00%，口径写进 `scripts/transcript-stats` 头注释与 `eval/fixtures/transcript-stats/README.md`；第四条会话 `plain-no-fork` 即为此。
- **Lesson**: 对照组分组条件必须写「与实验组无关」而非「看起来没被处理」——「没有 X 标记」是弱条件，同源但对端不标记的样本会漏进来；参照数字合理却讲不清分母，就去数 fixture。
- **Refs**: `scripts/transcript-stats:analyse_chains`、`eval/fixtures/transcript-stats/compact-chain/`、`eval/README.md`（`chains.baseline`）
