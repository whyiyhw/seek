### 新验收脚本忘了登记 selftest.sh → 验证车道拒跑、整卡 unverified 停人审（已机制化：登记制→发现制）
- **Saw**: 连续两张卡（c_6py7gh、c_zcygo2）的验证车道均拒绝跑新写的验收脚本——don't-ask 白名单只放行 `bash scripts/selftest.sh:*` 一条，直接 `bash scripts/<新脚本>` 不在名单，而新脚本又没登记进 selftest.sh 的 `target_of`/`ALL_NAMES`——整卡落 unverified 停人审，主会话手跑补证收口（#7848）
- **Why**: 覆盖与否寄托在「写完脚本的会话记得回到另一个文件加两行登记」上——写完脚本的那一刻任务感已完整，登记这步恰好最容易被忘
- **Fix**: selftest.sh 改发现制（c_aqbdaz）：名单 = `scripts/*.sh` 基名全量 − 显式排除表（逐项注理由）；裸跑非自测形态但自带自测 flag 的经 `dispatch_args` 字面量臂传参（kernel-deps → `--self-test`）。参数仍禁构造路径——只与 glob 派生基名比对、含 `/` 或 `..` 先拒，c_ck6djl 否决边界（白名单层禁通配、防 /tmp 穿越）原样保留
- **Lesson**: 依赖「作者记得去另一个文件补第二步」的机制，覆盖一定在第一步自我感觉完成时漏掉——把默认值倒过来：新工件默认被覆盖、不适宜的显式排除并注明理由，而不是默认不覆盖、记得了才登记
- **Refs**: `scripts/selftest.sh` 头注释，butler `docs/mechanisms.md` selftest 行，`scripts/eval-trend-test.sh`（wrapper 形态先例）
