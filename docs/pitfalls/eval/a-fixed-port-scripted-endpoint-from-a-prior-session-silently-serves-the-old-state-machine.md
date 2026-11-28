### 固定端口的 scripted endpoint 会跨会话存活 —— 复跑拿到的是上一版状态机
- **Saw**: 复核 E4 lane 成员资格：`PORT=18086 python3 eval/tools/fake_subagent_endpoint.py` 起端点跑 `eval/run.sh subagent-bg-resume`，得 `agent_continue_calls: want min=1, got 0` + `bg_resumed_lineage: 0`，疑产品回归；十分钟后重跑全绿。`lsof -iTCP:18086` 监听者是 19:33 的旧进程（文件 19:34 才写定）；新进程 75ms 即 `OSError: [Errno 48] Address already in use`，请求全打旧状态机。
- **Why**: 端点默认 `PORT=18086` 且长驻；worktree 复用、上轮进程还在 ⇒ 端口被占 + 新进程静默崩溃 ⇒ 测旧脚本——缺「续话」分支，失败形态指向产品而非 fixture。
- **Fix**: 复核前先 `lsof -nP -iTCP:<port> -sTCP:LISTEN`，被占就 kill 或换端口（`PORT=$((18086+RANDOM))` + `SEEK_EVAL_BASE_URL`）；证据链 `docs/evidence/c_3xyt7w/`（pid 47418）。
- **Lesson**: 固定端口夹具当有状态服务：起前查占用或用随机端口（`socket.bind(("127.0.0.1", 0))`）。「新进程起不来但命令跑完」靠显式探针（curl + `lsof`）拦，别信 `&` 后没报错。
- **Refs**: `eval/tools/fake_subagent_endpoint.py`、`eval/cases/plan-e2e/runner.sh`、卡 c_3xyt7w
