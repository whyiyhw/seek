### 机器提示必须被禁止延长回合——它每次都能给自己挣到一个新回合
- **Saw**: 给「确定性引导」接上循环头 + 结算点两个边界（与用户插话同缝）后，"每轮都说"的 Guide 让一次 Prompt 注入了 **397 条**消息，只有撞上 `MaxTurns` 才停。
- **Why**: 结算点是「模型已决定本轮结束」的位置，在此注入 user 消息会把 `break` 变成 `continue`，下一轮又产生同样的 silence/failure 形状的新 `TurnBrief` 让规则再次命中——**自增强闭环**。
- **Fix**: 引导只走**循环头**边界（`turn > 0`），结算点只接受**人类**注入；`pkg/agent/guidance.go` 注释写明「a hint must never extend a round」，测试 `TestGuidanceNeverExtendsTheRound` 用 always-fire Guide 断言 backend 调用数恒为 2。
- **Lesson**: 任何「系统自动产生输入」的机制在**终止决策点**必须关掉——判据是产生它的条件会不会因它的存在而继续为真；人的下一次干预需要人再动作一次，机器的下一轮判定只需要上一轮存在。
- **Refs**: `pkg/agent/agent.go`（结算点注释）、`pkg/agent/guidance.go`（包注释第 20+ 行）、`pkg/agent/guidance_test.go:TestGuidanceNeverExtendsTheRound`
