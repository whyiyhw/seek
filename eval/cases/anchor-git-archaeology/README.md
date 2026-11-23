# anchor-git-archaeology

外部锚（eval v3 A1）第 8 题：**git 考古**。8 个提交的 bundle（含一次改名、
一次行为修复），`MAGIC_TOKEN_7f3a9c` 恰好引入一次；agent 需 clone、
`git log -S` 定位、按全 SHA 作答。check 独立 clone 重算 SHA——黄金答案
不落 agent 世界（answer.txt 与重算值比对）。

- **探什么**：历史检索工具的驾驭（-S pickaxe 而非肉眼翻 log）+ 按全精度作答的纪律。
- **判分**：`check.sh` 重算 SHA 与 answer.txt 严格相等。
- **难度预期**：中。答短 SHA 或看错提交即红。
- **fixture 复算**：bundle 由生成脚本（8 提交定序）产出；golden/answer.txt 是生成时记录的引入提交 SHA。
