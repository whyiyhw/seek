### 压缩比的分母默认含 system prompt —— 每个会话的比值都被 system 占比钉住
- **Saw**: compact 链（card c_n7zsle）的分母是「父会话全历史 Σcontent 字节」，第一条链算出 0.3329。但全量 461 个 session 的 message 区第一条都是 `role=system`——system prompt 是几 KB 到 36 KB 的固定开销，每次重新下发、从不参与压缩。
- **Why**: 分母里的恒定大项把比值钉住：历史越长比值越接近真相，越短越接近「system 占比」本身。
- **Fix**: 分母排除 `role=system`（`content_bytes` 只累加非 system 消息）；分子剥掉 `/compact` 外包的 76 字节 `SUMMARY_PREFIX` 样板（`"Here is a summary of our earlier conversation. Continue from this context:\n\n"`）再量。fixture 比值 0.8208→0.3815（真数据 0.3329→0.0830）；`parent.jsonl` 刻意带 system 消息反向验证：不排除则 0.3815 塌到 0.3194，断言转红。
- **Lesson**: 算比率时「总量」必须与被测对象同类——不随会话长度变化的项（system prompt 等外壳）进分母就是常数偏移。
- **Refs**: `scripts/transcript-stats:load_sessions`（`content_bytes` / `head_bytes` / SUMMARY_PREFIX）、`eval/fixtures/transcript-stats/README.md`、`internal/tui/update_agent.go:handleCompactDone`
