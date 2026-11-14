# serve — 运行与停机契约（M6a）

`internal/controlplane/serve.go` 是远程控制平面的常驻入口骨架（PRD
[`feature-remote-control-plane.md`](../prd/feature-remote-control-plane.md) §5 M6、D-C1/D-C4）。
本目录沉淀它的**运维契约**：生命周期、drain 预算、退出码、build-tag 依赖隔离，
以及 launchd / systemd 示例单元。用户侧使用指南（绑定、指令、web token 换票）见
M7 的 `docs/guide-serve.md`。

**当前状态（M6b + M2）**：生命周期机制（M6a）+ `seek serve` CLI 接线（M1b/M6b，
`internal/servecli`）均已落地并有测试锁定；入站 handler 已从 M1b 的丢弃桩换成
M2 路由器（`internal/controlplane/router.go`：EventID 去重 → DirectAt 过滤 →
`/bind` `/projects` `/switch` `/new` `/cancel` 指令与 ConvID→binding 路由 →
M3 执行器 → session 回写 → 按 frontend 前缀回发）。默认构建注册的 frontend 为
`fake`（起停自检用）与 `web`（浏览器聊天，MVP 已交付）；示例单元的
`ExecStart=... --frontend=web` 即真实可用形态。飞书 frontend（M4/M5）已于
2026-11-13 撤销排期——任何聊天渠道经 `Frontend` 端口接入，不绑定具体厂商。
config 的可选 `serve` 段
（projects / max_concurrency / yolo，默认 yolo=false）见
[`internal/config`](../../internal/config/config.go)；旧 config 无该段照常工作。

## 生命周期

```
启动   N 个 Frontend.Run 各自 goroutine → 扇入同一个有界 channel（QueueDepth）
稳态   dispatch 循环：每条消息交给 handler 自己的 goroutine
        （真实并发上限由执行器 M3 的全局 cap 管，serve 不重复设限）
停机   触发：serve ctx 取消（main 把 SIGINT/SIGTERM 映射成 cancel）
        或所有 frontend 自身出错退出
  1    停止入站：取消 frontend ctx；晚到的 in-flight push 仍会被 drain 消费
  2    drain（≤ DrainWait）：继续 dispatch 队列里已有的消息，直到 channel
        关闭（所有 Run 已返回 ⇒ 队列可证为空）或预算耗尽；期间 handler 的
        ctx 仍然存活——在途工作可以做完，取消只发生在预算耗尽那一刻
  3    settle（≤ HandlerGrace）：等 handler goroutine 收尾；宽限耗尽则取消
        其 ctx，再给一个 HandlerGrace 让其观察取消
```

**handler 契约**：handler 必须尊重自己的 ctx。serve 杀不掉 goroutine——
不理会取消的 handler 把最坏停机时间拖到 `DrainWait + 2×HandlerGrace`，
之后 serve 照样返回，卡死的 goroutine 随进程退出由 supervisor 兜底。

**Run 契约**：frontend 的 `Run` 在 ctx 取消后应尽快返回（阻塞式
`Start(ctx)` 即此形状，PRD §6 纸面验证③）。赖着不返回的 Run 使 channel 永不关闭、队列
永远「不可证为空」——停机在 drain 预算耗尽后以 exit 1 收场。

## 退出码（`controlplane.ExitCode`）

| 码 | 含义 | supervisor 动作（示例单元的语义） |
|---|---|---|
| 0 | 干净停机：队列排空、无 frontend 失败 | 不重启（launchd `KeepAlive` 除外，见下） |
| 1 | `ErrDrainIncomplete`：drain 预算耗尽，在途工作被丢弃 | 重启（服务自身可恢复） |
| 2 | frontend 层失败：全部 frontend 出错退出 / 配置错误 | 重启 + 告警（持续 2 = 配置或凭证问题） |

两类失败叠加时（全部 frontend 挂了 **且** drain 丢工）错误会 join，
ExitCode 取 1——「丢了工作」是更响的事实。

## build-tag 依赖隔离（D-F1）

飞书 SDK（`github.com/larksuite/oapi-sdk-go/v3`）只允许活在
`internal/controlplane/feishu/` 的 `//go:build feishu` 文件里：

```sh
go build ./...                                  # 默认构建：feishu 包只编译 doc.go，零 SDK
go test -race ./internal/controlplane/...       # 含 buildtags_test：默认构建排除证明（go/build 机械断言）
go test -race -tags feishu ./internal/controlplane/...  # tag 打开后：占位 frontend 真实过扇入
```

`buildtags_test.go` 在**默认 CI** 里断言 feishu 包的 `GoFiles == [doc.go]`：
任何不带 tag 的 .go 文件混进该包（SDK 泄进默认二进制的唯一路径）都会先撞红
这条测试。tagged 侧的对照测试（`-tags feishu` 下占位 frontend 经真实扇入跑
隔离断言）不在默认 CI 内，某聊天渠道真落地时应把对应 `-tags` 加进 CI 矩阵。

## supervisor 预算公式

单元里的强杀超时必须大于 serve 的最坏停机时间，否则 supervisor 的 SIGKILL
会先于 serve 自己的预算收尾：

```
强杀超时（ExitTimeOut / TimeoutStopSec） > DrainWait + 2 × HandlerGrace
默认：10s + 2×5s = 20s  →  单元取 30s
```

改 `ServeConfig` 预算时同步改单元文件（`deploy/seek-serve.plist` 与两份示例同源，改预算时三处一起动）。

## 常驻化安装（macOS）

`deploy/seek-serve.plist` 是可安装模板（`{{SEEK_BIN}}`/`{{SEEK_LOG}}` 占位符，
契约参数与上面的示例 plist 一致），`scripts/install-serve.sh` 负责校验、
渲染与加载：

```sh
scripts/install-serve.sh install              # 校验二进制 → 渲染 → launchctl load
scripts/install-serve.sh install --dry-run    # 只打印动作与渲染结果，不写不加载
scripts/install-serve.sh uninstall            # launchctl unload + 删 plist（日志保留）
scripts/install-serve.sh status               # 最近退出码 / PID
```

默认二进制 `~/.local/bin/seek`（`SEEK_BIN` 覆盖）、日志
`~/Library/Logs/seek-serve.log`（`SEEK_LOG` 覆盖，stdout/stderr 合流写同一
文件）。launchd 不展开 `~`，占位符必须由脚本渲染成绝对路径——不要手工
cp 模板。安装后用 `launchctl list | grep seek-serve` 确认加载，从日志拿
web frontend 的启动横幅（带 token 的 URL）：`tail -f ~/Library/Logs/seek-serve.log`。

模板 ProgramArguments 固定 `--frontend=web`（默认构建即含）；未来某聊天渠道
落地后在模板里重复一行 `<string>--frontend=<name></string>` 即可多开，
重跑 install 覆盖。

## 文件

- [`seek-serve.plist`](seek-serve.plist) — macOS launchd 用户 agent 示例（契约参考）
- [`seek-serve.service`](seek-serve.service) — Linux systemd 用户服务示例
- [`../../deploy/seek-serve.plist`](../../deploy/seek-serve.plist) — 可安装模板（占位符版，install-serve.sh 渲染）
- [`../../scripts/install-serve.sh`](../../scripts/install-serve.sh) — macOS 常驻化安装/卸载脚本
