### 裸赋值会清 $?——check 结果被 solved=false 吃掉
- **Saw**: anchor-crossharness.sh 首次烟测里，什么都不做的假 harness 跑两题全报 `solved=true`；手工以同参数跑 check.sh 却正确退 1。
- **Why**: `( subshell )` 之后先写了 `solved=false` 再读 `$?`——裸赋值本身就是一条命令，退出码恒 0，把 subshell 的真实退出码覆盖了。
- **Fix**: 退出码落地到具名变量再赋值：`check_rc=$?` → `solved=false` → `[ "$check_rc" -eq 0 ] && solved=true`。（提交见 eval v3 A3。）
- **Lesson**: bash 里 `$?` 只在「紧接着」读才是真的；中间插一条赋值、一条 `[ ]`、甚至一声 `echo` 都会改写它。捕获意图先落变量，同仓先例：坑 5「管道里 tail -1 会吞错误行」。
- **Refs**: eval/tools/anchor-crossharness.sh 任务循环注释。
