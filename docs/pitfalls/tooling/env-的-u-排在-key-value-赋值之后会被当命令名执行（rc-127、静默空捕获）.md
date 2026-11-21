### env 的 -u 排在 KEY=VALUE 赋值之后会被当命令名执行（rc=127、静默空捕获）
- **Saw**: eval-home-isolation 自测首跑六断言全红、环境捕获文件为空——假二进制从未执行；同会话第二稿把调用方参数挪到 helper 最前仍红：helper 自带的 `-u SEEK_EVAL_EXTRA_ARGS` 又落到了调用方 KEY=VALUE 之后
- **Why**: `env` 遇到第一个非选项参数（KEY=VALUE 或命令名）即停止解析选项，其后出现的 `-u` 不再是选项而是下一个待执行程序的名字，报 `env: -u: No such file or directory`（rc=127）；自测把 stdout/stderr 重定向吞掉后，表象只剩"子进程什么都没发生"
- **Fix**: 自测每条 env 行按「全部 -u → 全部 KEY=VALUE → 命令」固定顺序写，并放弃参数经 `"$@"` 混排的 helper——混排无法保证选项块整体前置
- **Lesson**: 写 `env -u A X=1 … cmd` 时把所有 `-u` 当"必须前置的块"；测试里子进程"零副作用"先查 rc=127 与 stderr 再怀疑断言
- **Refs**: `scripts/eval-home-isolation-test.sh`（顺序注释），`scripts/eval-ablate-test.sh` 的 `env -u` 先例
