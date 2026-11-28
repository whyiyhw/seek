### jq 流式解析把一行里多个 JSON 值照单全收，JSONL 完整性断言只靠 jq 校验必漏检挤行交错
- **Saw**: eval 结果并发写自测（c_ckla3l）的行完整性断言，「每行 `jq -e .`」抓不住最要命的坏形态：两个对象挤在同一物理行（`{"a":1}{"b":2}`）时 jq 照单全收解析成两个值、退出码 0——并发写交错最常见的幸存形态。
- **Why**: jq 是流式解析器，换行只是空白字符不是值分隔符，一个物理行放两个完整 JSON 值完全合法；撕裂半行永远拼不成合法值，jq 全文解析必炸——jq 只抓撕裂、抓不住挤行。
- **Fix**: 三查断言：行数对账（`wc -l` 对期望值，抓丢写/重复写）、无空行（撕裂残留）、JSON 值总数==行数（`jq -s 'length'`，挤行时值多出行数）；`jq -s` 全文解析非零退出抓撕裂。落 `scripts/eval-rounds-test.sh` 的 `rows_intact`。
- **Lesson**: 「逐行可解析」两方向漏检相反：jq 抓不住一行多值，行数对账抓不住值分裂——必须同时做，缺一即假绿；凡 JSONL 完整性断言（session 落盘、eval 结果、transcript）都适用。
- **Refs**: `scripts/eval-rounds-test.sh`；`eval/README.md` §C3
