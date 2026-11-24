# anchor-cli-spec

外部锚（eval v3 A1）第 3 题：**按规格实现**。SPEC.md 定义一个小 CLI
（`--min-score`/`--tag` 过滤 + 字符串升序输出 + 缺输入退 2），entries.json
已在；agent 从零写 filter_entries.py。三组黄金 stdout + 一条错误路径断言。

- **探什么**：书面规格 → 代码的忠实度（默认值、`>=` 边界、字符串序、stdout 纯净性、错误码）。
- **判分**：`check.sh` 三组 diff 黄金 + 缺输入必须非零退出。黄金在 `golden/`（ws 外）。
- **难度预期**：中。逻辑简单但契约点多（5 个可失分面），正是 harness 差异敏感的地方。
