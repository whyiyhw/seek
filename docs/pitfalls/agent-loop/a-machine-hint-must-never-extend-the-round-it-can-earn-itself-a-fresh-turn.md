### 机器提示必须被禁止延长回合——它每次都能给自己挣到一个新回合
- **Saw**: 给「确定性引导」接上两个边界（循环头 + 结算点，与用户插话同缝）后，测试里一个"每轮都说"的 Guide 让一次 Prompt 注入了 **397 条**消息，且只有撞上 `MaxTurns` 才停。
- **Why**: 结算点是「模型已经决定本轮结束」的位置。在此注入一条 user 消息会把 `break` 变成 `continue`；下一轮又产生新的 `TurnBrief`（同样的 silence/failure 形状），于是规则再次命中——**自增强闭环**。用户插话不会这样，因为人是外部的、一次性的；机器的判定是内部的、可重复的。
- **Fix**: 引导只走**循环头**边界（`turn > 0`），结算点只接受**人类**注入。`pkg/agent/guidance.go` 的注释写明「a hint must never extend a round」，并有一条测试 `TestGuidanceNeverExtendsTheRound` 用 always-fire 的 Guide 断言 backend 调用数恒为 2。
- **Lesson**: 任何「由系统自动产生输入」的机制，在**终止决策点**上都必须关掉。判据不是「这条消息有没有用」，而是「产生它的条件会不会因为它的存在而继续为真」——如果是，就是循环。人和机器的区别正在这里：人的下一次干预需要人再动作一次，机器的下一次判定只需要上一轮存在。
- **Refs**: `pkg/agent/agent.go`（结算点注释）、`pkg/agent/guidance.go`（包注释第 20+ 行）、`pkg/agent/guidance_test.go:TestGuidanceNeverExtendsTheRound`
