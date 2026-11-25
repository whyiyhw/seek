### 压缩比的分母默认含 system prompt —— 每个会话的比值都被 system 占比钉住
- **Saw**: 给 `scripts/transcript-stats` 加 compact 链（card c_n7zsle）时，用「父会话全历史 Σcontent 字节」当压缩比分母。真数据上第一条链算出 0.3329，看上去不荒谬，所以没人怀疑。但全量 461 个 session 里，**461 个**的 message 区第一条都是 `role=system`，system prompt 是几 KB 到 36 KB 的固定开销——它每次重新下发、从不参与压缩。
- **Why**: 分母里塞进一个恒定的、与被测对象无关的大项，比值就被它钉住了：历史越长，system 占比越小、比值越接近真相；历史越短，比值越接近「system 占比」本身。同一个摘要放在不同长度的会话里会得到完全不同的「压缩比」，而这个数被用来判断「压缩有没有用」。
- **Fix**: 分母排除 `role=system`（`content_bytes` 只累加非 system 消息）。同时把分子也收紧：`/compact` 的摘要外面我们包了 76 字节的 `SUMMARY_PREFIX` 样板（`"Here is a summary of our earlier conversation. Continue from this context:\n\n"`），样板剥掉后才量摘要本身。两处修正让 fixture 的比值从 0.8208 变成 0.3815（真数据从 0.3329 变 0.0830）。fixture 里 `parent.jsonl` 刻意带一条 system 消息，反向验证过：分母不排除 system → 均值 0.3815 塌到 0.3194，断言转红。
- **Lesson**: 算比率时，「总量」必须与被测的量同类。system prompt、样板字符串、我们自己包的外壳都不是被测对象的产出——它们进分母就会把比值变成「外壳占比」的伪装。判据很简单：**这个项会不会随时间/会话长度变化？** 不会变化的项进比值，就是给分母加了个常数偏移。
- **Refs**: `scripts/transcript-stats:load_sessions`（`content_bytes` / `head_bytes` / SUMMARY_PREFIX）、`eval/fixtures/transcript-stats/README.md`（反向验证表）、`internal/tui/update_agent.go:handleCompactDone`（样板串的产地）
