### 纯搬移拆文件时 `git diff -M` 检不出 rename——一份源拆成两份，配对是一对多
- **Saw**: 卡 c_3mny65 把 `cmd/seek/main.go` 的两块（planBridge / subagent glue）各拆成新文件，卡面验收写的是「`git diff -M` rename 检出」；提交后 `git diff -M -C --find-copies-harder --summary` 仍报两个 `create mode`，没有一处 `rename`，`--stat` 只有 `main.go −662 / planbridge.go +315 / subagent_glue.go +386`。
- **Why**: rename 检测是「一个删除 ↔ 一个新增」的相似度配对；一次拆成 N 个新文件时，被删的 662 行要同时对上两个新增块，任何单侧相似度都低于阈值。`-C` / `--find-copies-harder` 只扩大候选集，不改一对一配对模型，所以拆分永远显示成 delete + create。
- **Fix**: 等值口径换成「区段 diff 为空」——`diff <(git show HEAD:cmd/seek/main.go | sed -n '122,161p;196,456p') <(sed -n '15,315p' cmd/seek/planbridge.go)` 无输出 ⇒ 函数体零改动；再对 main.go 剩余部分做「挖掉两块后与 HEAD 逐行比对（只允许空行规整 + 少两条已搬走的 import）」证明没有夹带。
- **Lesson**: 「纯搬移可核」的手段随拆分形态变——整文件搬家（`envgate.go`）用 `-M`，一块代码分到多个新文件用区段 diff。把验收写法钉死成 `-M`，会让正确的纯搬移看起来像没做。
- **Refs**: `cmd/seek/main.go`, `cmd/seek/planbridge.go`, `cmd/seek/subagent_glue.go`; 整文件搬家先例 `cmd/seek/envgate.go`（0a66450）
