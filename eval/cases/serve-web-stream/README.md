# serve-web-stream

**Probes**: eval v2 三条承重轴之一（[`docs/prd/archive/feature-eval-v2.md`](../../../docs/prd/archive/feature-eval-v2.md)
§2.2 / §三-E5）——`seek serve --frontend=web` 平面的**端到端线契约**：
`/msg /bind` → 执行器派生真 seek 子进程跑一轮 → W1.4 SSE 事件族 →
`/rpc session/list` 落盘可读。这是 2026-11-20/21 W5 组（流事件族 /
ContextMeter / 审批中枢 / 会话面板）改动面最大的平面，本例把它纳入
eval 保护网——正是 E5 的存在理由（PRD §1.2 零覆盖清单「serve/web
平面」条）。

**双断言轴**（判分全部在 `eval/run.sh` 的 `extract_metric` 复算，
断言 **wire 形状**，不匹配任何散文）：

| 轴 | 断言 | 锚点 |
|---|---|---|
| 事件序列·帧序 | 同一 run 的 `delta`/`usage` 帧全部先于带 `run`+`session_id` 的收口默认帧（`sse_frame_order_ok`） | `webfrontend_stream.go` 头注释的 events…reply 不变量（每 conv 至多一个 live run，收口 Send 在 RunStream 返回后） |
| 事件序列·累积 | `delta` 帧 `text` 逐帧拼接 == 收口帧 `text`（`sse_delta_accum_ok`；假端点发**两段** delta `web plane`+` stream ok`，顺序错了拼接就不等） | 执行器 `seekOutcome.replyBuf` 与 `Send` 收口同源 |
| 事件序列·收口替换 | 收口默认帧带非空 `run`（等于 delta 帧的 run）与非空 `session_id`（`sse_reply_closeout_ok`） | `Send` 的 W1.4 close-out（MVP envelope 纯增量） |
| 会话落盘 | `/rpc session/list` 的 `sessions[].sid` 命中收口帧的 `session_id`（`session_list_hits ≥ 1`） | 子进程落 `$SEEK_HOME/sessions/`，`methods.NewSessionsFromEnv` 同一 env 阶梯读回 |

进程级结果（POST 非 202/200、bind 回执缺失、run 收口超时、serve 排干
非 0）经 `runner_fact` 行上浮，`max_nonzero_exits = 0` 兜底——**崩溃轮 =
红轮**，分母照计（E1 口径）。

**启动/收尾复用 [`scripts/serve-smoke-test.sh`](../../scripts/serve-smoke-test.sh)
先例**（卡 c_snerqc，2026-11-15 安全面返工版）：起平面后轮询启动行判活、
token 从 `<SEEK_HOME>/serve/web.token` 读、收尾 SIGTERM → 排干 → 退出码
必须 0、`DRY_RUN=1` 只打印步骤。不另造 fixture 脚手架——serve 生命周期
契约与 smoke 同款，只把断言面从「未绑定 unboundHint」换成「绑定后完整
跑一轮 + 落盘」。模型端点复用共享的 [`eval/tools/fake_openai_compat.py`](../tools/fake_openai_compat.py)
（本卡给它加了 opt-in 的 `DELTAS` 多段 delta 旋钮，缺省行为字节不变）。

**形态**: `runner.sh`（见文件头注释）驱动**真 serve 子进程**：受测二进制
以 `seek` 之名进沙盒 `bin/` 置于 PATH 首位（执行器 `SeekPath` 缺省走
PATH 查找），`SEEK_HOME` 指向本例沙盒（继承 run.sh 的隔离钉，再收窄到
`eval/tmp/serve-web-stream/home`，每轮全清）。SSE 用 `curl -N` 全程录制，
runner 只做**帧配对归一**（`event:`/`data:` → `sse_frame` JSONL 行），
判分在 run.sh。依赖 `curl` + `python3` + `jq`。

**假 key 车道（卡面硬规定，[`docs/prd/v8.md`](../../docs/prd/v8.md) §9.1
无人值守纪律）**: runner 内把 serve 进程（及其派生子进程）的车道钉死为
GLM 兼容车道 → `127.0.0.1` 假端点：五大车道凭证 env 一律清空（空值 =
`os.Getenv` 视为未设），`GLM_API_KEY`/`GLM_BASE_URL` 指向假端点。外层
`SEEK_EVAL_*` 车道旋钮对本例**不生效**——本例测平面线契约，不测模型
选择，任何车道跑出来的线形状必须相同。**真 key 发版道（E6）接缝**：
同 env-in/env-out 原理——不清空车道凭证、不设 GLM 假端点，子进程即走
真实车道，断言面不变（真流多 delta、多 turn，`min_sse_delta_frames: 2`
照常成立；届时按 PRD §六.3 在 5 轮上重标定 gate）。

**三态与 rounds 口径（E1 兼容）**: runner 每轮开头 `rm -rf` 沙盒，一轮
= 一次完整 serve 生命周期，`--rounds N` 直接可用；单轮红/绿走 legacy
判定，多轮走 `min_pass_rate` 聚合，`record_only` 翻转不影响 runner
行为。假 key 车道下全链确定性（假端点固定两段 delta），预期通过率 1.0，
gate 取缺省 1.0——**这不是放水**：本例在假 key 车道测的是线契约回归，
红即真回归（W5 组再动 webfrontend_stream/conv/rpc 任何一处即翻红）。

**指标**（六项均为 content-conditional：流里有 `sse_frame` 行才进
metrics 对象，非 serve 用例的行形状字节不变——`scripts/eval-ablate-test.sh`
的 no-env 字节等价守卫因此保持绿）:

- `sse_delta_frames` / `sse_usage_frames` —— 计数（`min 2` / `min 1`；
  两段 delta 是累积断言的非平凡性来源）。
- `sse_frame_order_ok` —— 帧序不变量 1/0。
- `sse_delta_accum_ok` —— 累积等值 1/0。
- `sse_reply_closeout_ok` —— 收口形状 1/0。
- `session_list_hits` —— 落盘命中数（`min 1`）。

**邻接已验证事实（本例不锁）**: 401 token 门/GET 200 HTML 面（Go 测试
`TestServePlane_WebSmoke` 已锁）；approval 帧族（`webfrontend_approval*`
测试已锁）；topic 流（W3.2，`webfrontend_topic_test.go`）。本例只锁
「绑定后完整一轮的流线 + 落盘」，与 Go 单测的分工见 PRD §1.1 三层职责表。

**机制预算答问**（PRD §2.5「范围之外」口径）：不引入新测试框架/外部包
——bash + jq + 既有 python3 假端点；唯一新增系统工具依赖 **curl**（SSE
消费，与 jq 同级的系统二进制，git-bash 自带；缺失即启动退出并报错，不静默）。
run.sh 增量 +88 行（6 个 extract_metric case + 一个条件合并块），对非
serve 用例的输出**字节零影响**（content-conditional + ablate 守卫重跑验证）。
runner 219 行，判分全部在 extract_metric 复算（runner 只录流配对，不解判）
——「判分必须可复算」（§2.5）成立。启动/收尾不另造脚手架：serve 生命周期
契约复用 serve-smoke 先例，模型端点复用共享 fake 工具（只加 opt-in 旋钮）。
