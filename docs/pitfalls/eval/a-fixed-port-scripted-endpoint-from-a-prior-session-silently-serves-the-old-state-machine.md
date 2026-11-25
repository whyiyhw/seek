### 固定端口的 scripted endpoint 会跨会话存活 —— 复跑拿到的是上一版状态机
- **Saw**: 复核 E4 lane 成员资格时，用 `PORT=18086 python3 eval/tools/fake_subagent_endpoint.py` 起端点再跑 `eval/run.sh subagent-bg-resume`，得到 `agent_continue_calls: want min=1, got 0` + `bg_resumed_lineage: 0`——看着像产品回归。同一份代码十分钟后重跑全绿。`lsof -iTCP:18086` 显示监听者是**19:33** 启动的旧进程（文件 19:34 才写定），我新起的进程 75ms 就 `OSError: [Errno 48] Address already in use` 退出了，请求全打到了旧状态机上。
- **Why**: 端点默认 `PORT=18086`（写死，方便文档里的复制粘贴），又是长驻 python 进程；worktree 被复用、上一轮会话的进程还在时，端口被占 + 新进程静默崩溃 ⇒ 测的是旧脚本。而旧脚本恰好缺「续话」分支，于是失败形态指向产品而不是 fixture。
- **Fix**: 复核前先 `lsof -nP -iTCP:<port> -sTCP:LISTEN`；端口被占就 kill 或换端口（`PORT=$((18086+RANDOM))` + 对应 `SEEK_EVAL_BASE_URL`）。本轮证据链见 `.butler-evidence-c_3xyt7w/`（旧进程 pid 47418 / 19:33 启动）。
- **Lesson**: 固定端口的测试夹具必须当成**有状态的外部服务**对待——起之前查占用，或用随机空闲端口（`eval/cases/plan-e2e/runner.sh` 就是这么做的：`socket.bind(("127.0.0.1", 0))` 取空闲端口）。另外，「新进程起不来但命令仍跑完」这条静默路径要靠显式探针（curl 就绪检查 + `lsof`）拦住，别信 `&` 之后没报错。
- **Refs**: `eval/tools/fake_subagent_endpoint.py`（默认 `PORT=18086`）、`eval/cases/plan-e2e/runner.sh`（随机端口的正确做法）、卡 c_3xyt7w
