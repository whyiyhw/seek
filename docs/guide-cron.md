# seek cron — 跨平台启用指南 / Cross-platform setup guide

`seek cron` 让定时任务、模型自调用唤醒、外部系统触发都跑在你机器上的 OS 调度器里——**seek 自身没有任何常驻进程**。这意味着启用 = 教 OS 调度器每分钟跑一次 `seek cron tick`，剩下的 seek 自己搞定。

> `seek cron` runs your scheduled prompts, model-self-scheduled wakeups, and external triggers from your machine's native scheduler — **seek itself has no resident process**. Setup means teaching the OS scheduler to run `seek cron tick` every minute; seek handles the rest.

本指南覆盖三平台的 schedule 注册、env 注入、OS 通知现状，以及外部触发文件桥。CLI 参考见 `seek cron help`；架构与设计见 [`docs/prd/feature-routines.md`](./prd/feature-routines.md)。

---

## 1. 先注册一个任务 / Register a job first

```bash
# 每天检查 main 分支有没有失败的 CI
seek cron create --name ci-watch --at @daily \
  --cwd ~/code/myproj \
  'check main branch CI status; if any failed, summarize'

seek cron list   # 看看注册了什么
seek cron run ci-watch   # 立刻跑一次，绕过调度
```

注册到 `~/.seek/cron/jobs.jsonl`。但**还不会自动跑**——OS 调度器还没启动 tick。

> Job goes into `~/.seek/cron/jobs.jsonl`, but **nothing fires yet** — the OS scheduler isn't calling tick.

---

## 2. 接 OS 调度器 / Wire up the OS scheduler

### macOS — launchd

```bash
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.seek.cron.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.seek.cron</string>
  <key>ProgramArguments</key>
    <array>
      <string>/usr/local/bin/seek</string>
      <string>cron</string>
      <string>tick</string>
    </array>
  <key>StartInterval</key><integer>60</integer>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
EOF
launchctl load ~/Library/LaunchAgents/com.seek.cron.plist
```

把 `/usr/local/bin/seek` 改成 `which seek` 的实际路径。卸载：`launchctl unload ~/Library/LaunchAgents/com.seek.cron.plist`。

### Linux — systemd user units

```bash
mkdir -p ~/.config/systemd/user

cat > ~/.config/systemd/user/seek-cron.service <<'EOF'
[Unit]
Description=seek cron tick

[Service]
Type=oneshot
ExecStart=%h/.local/bin/seek cron tick
EnvironmentFile=-%h/.seek/cron/env
EOF

cat > ~/.config/systemd/user/seek-cron.timer <<'EOF'
[Unit]
Description=Fire seek cron tick every minute

[Timer]
OnBootSec=1min
OnUnitActiveSec=1min

[Install]
WantedBy=timers.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now seek-cron.timer
```

`EnvironmentFile=-` 前缀的 `-` 意味"文件不存在也不报错"——配合下一节的 env overlay 一鱼两吃。检查：`systemctl --user list-timers`、`journalctl --user -u seek-cron.service`。

### Windows — Task Scheduler

```powershell
schtasks /create /tn "seek cron tick" /tr "seek cron tick" /sc minute /mo 1 /ru "$env:USERNAME"
```

或 GUI：**任务计划程序 → 创建基本任务**，触发器选 "每天 → 重复任务每 1 分钟"，操作选"启动程序"，程序 `seek`，参数 `cron tick`。卸载：`schtasks /delete /tn "seek cron tick" /f`。

---

## 3. 让子进程拿到 API Key / Subprocess env

**核心问题：OS 调度器拉起的进程 env 极简**——`launchd` 通常只给 `PATH=/usr/bin:/bin`，`systemd` 用户单元只继承 `EnvironmentFile=` 列出的，classic `cron(1)` 连 `HOME` 都没有，Windows Task Scheduler 也类似。你 `.zshrc` / `.bashrc` 里的 `DEEPSEEK_API_KEY`、`PATH` **完全不会**自动跨过这条边界。

> The OS scheduler hands `seek cron tick` a near-empty environment. Your interactive shell's `DEEPSEEK_API_KEY` and `PATH` will **not** cross that boundary automatically.

不修等于：每次定时跑都触发 auth failure，silent。

### 解法：`~/.seek/cron/env` 覆盖文件 / Overlay file

写一个 dotenv-style 文件，seek 自动叠加到子进程 env 上：

```bash
# ~/.seek/cron/env（macOS / Linux / WSL）
# %USERPROFILE%\.seek\cron\env（Windows）

# 注释以 # 开头；空行 OK
DEEPSEEK_API_KEY=sk-...
PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
# Linux 桌面通知额外需要：
# DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus
```

格式规则：
- 一行一个 `KEY=VALUE`
- 平衡引号 `"…"` / `'…'` 被剥离（值内空格保留）
- **不**做 shell 展开（`$HOME` 是字面三个字符）
- 重复 KEY 后写赢
- 解析错（缺 `=` / 空 KEY）→ **明确报错**，spawn 失败。比"静默继续"安全得多——总比每次跑都因为掉了 API key 然后悄悄 fail 强

### systemd 用户：一文件双用

guide-systemd 那段 `EnvironmentFile=-%h/.seek/cron/env` 让 systemd 用同一个文件 export 它自己的 env（影响 `seek cron tick` 进程级 env）；seek 自己又会从 `LoadEnvFile` 读一遍合并到子进程。一份文件管两层。

### 验证

```bash
seek cron run <job-name>   # 立刻跑一次，看到 auth fail 就是 env 没接上
tail -F ~/.seek/cron/runs/*.jsonl   # 看最近 run 的 stderr
```

---

## 4. OS 通知现状 / Notification status

| 平台 | 实现 | 状态 |
|---|---|---|
| macOS | `osascript -e 'display notification ...'` | ✅ Notification Center banner |
| Linux | `notify-send` (libnotify) | ✅ 桌面（`$DISPLAY`/`$WAYLAND_DISPLAY` 都空时自动 no-op） |
| Windows | 无 | ⚠️ **当前 no-op**——`internal/routines/notify_windows.go` 显式返回 nil |

Windows 用户启用 `--notify always` 不会收到任何弹窗——这是 v0.6.1 已知限制。两个可行路径都有代价（BurntToast PowerShell 模块要 `Install-Module`；WinRT 要 CGO，破坏 zero-dependency 立场）。v0.6.x dot 计划补 BurntToast 适配，详见 [`feature-routines.md §3.8`](./prd/feature-routines.md)。

**临时方案**：Windows 用户依赖 `seek cron list` 看 `last_status` + `tail -F %USERPROFILE%\.seek\cron\runs\*.jsonl` 查最近运行，而不是依赖通知。

> Windows users on v0.6.1 receive no popups — check `seek cron list` and tail `~/.seek/cron/runs/*.jsonl` instead.

### Headless 服务器

Linux notify 在 `$DISPLAY` 和 `$WAYLAND_DISPLAY` 都为空时自动 no-op，不会刷 stderr。macOS headless（虽然罕见）`osascript` 也会失败，目前会写 `WARN: notify failed: ...` 到 run record，不阻塞 cron 跑——任务跑了，只是没弹窗。

### 移动端推送 webhook / Mobile push (v0.7.0 柱 M)

桌面通知只在你坐在电脑前才有用。**push webhook** 让 cron / trigger 的终态**额外**通过 HTTPS POST 推到你自选的渠道——离开电脑也能收。桌面通知**不受影响**，webhook 是叠加的旁路。

在 `~/.seek/config.json` 配置：

```jsonc
{
  "push_webhooks": [
    {
      "url": "https://ntfy.sh/my-seek-topic",   // 你自己的 topic
      "format": "ntfy",                          // ntfy | slack | discord | feishu | raw（默认 raw）
      "events": ["cron.failed", "cron.killed"]   // 省略 = 全部事件
    }
  ]
}
```

事件名：`cron.completed` / `cron.failed` / `cron.killed`、`trigger.completed` / `trigger.failed`。

**最快上手（ntfy.sh，开源 + 免费 + 有 iOS/Android app）**：
1. 手机装 [ntfy](https://ntfy.sh) app，订阅一个只有你知道的 topic，例如 `my-seek-7f3a`。
2. config 里 `"url": "https://ntfy.sh/my-seek-7f3a"`，`"format": "ntfy"`。
3. 验证：`seek cron config check --probe`（往每个 webhook 发一条真实测试消息）。

**飞书 / Lark（国内推荐）** 两种接法:
- **自定义机器人**:群里加「自定义机器人」拿到 URL(形如 `https://open.feishu.cn/open-apis/bot/v2/hook/<id>`),`"format": "feishu"`。坑:① 开了**自定义关键词**时推送文本须含该关键词;② 飞书对关键词/签名错误**返回 HTTP 200 + body `code≠0`**,seek 只看 HTTP 状态,这类逻辑错抓不到——建议用**无签名**机器人,先 `--probe` 确认。
- **飞书流程(Flow)trigger**:URL 形如 `https://www.feishu.cn/flow/api/trigger-webhook/<id>`,`"format": "feishu-flow"`(payload `{"msg_type":"text","content":{"text":{"title":…,"msg":…}}}`,title→标题、msg→正文)。这套 payload 由你的 Flow 自定义;若你的 Flow 期望别的形状,改用 `"format": "raw"`(发 `{event,title,body}`)在 Flow 里映射字段。

```bash
seek cron config check          # 离线校验 scheme + format
seek cron config check --probe  # 额外发真实测试通知，确认渠道可达
```

要点 / Notes：
- **私网/LAN 地址放行**：`http://192.168.x.x/...`、自托管 ntfy、内网 Slack relay 都行——这是你在自己 config 里写的 outbound，不是模型驱动的 SSRF（与 `webfetch` 的私网拦截**不同**）。
- **best-effort**：webhook 失败只写 WARN 到 stderr，**绝不**影响 cron 任务本身（任务照跑、run record 照写）。5xx 自动重试一次。
- **events 独立于 `--notify`**：`--notify never` 的任务（不要桌面弹窗）仍可通过 webhook `events` 推送失败到手机——桌面和远端是两个正交开关。
- **隐私**：body 即桌面通知的 body，会发给你**自选**的第三方；敏感场景推荐 ntfy 自有 topic 或自托管。

> Desktop popups only help when you're at the machine. `push_webhooks` additionally POSTs cron/trigger outcomes to a channel you pick (ntfy/Slack/Discord/Feishu/raw). Best-effort, never blocks the run, private/LAN URLs allowed. Verify with `seek cron config check --probe`.

---

## 5. 外部触发 / External triggers (file bridge)

CI、IDE 插件、shell 脚本可以写 JSON 到 `~/.seek/cron/triggers/`，下一次 tick 消费 + 删除：

```bash
# CI 跑完通知 seek 关注一下
cat > ~/.seek/cron/triggers/ci-$(date +%s).json <<EOF
{
  "trigger_id": "ci-build-1234",
  "prompt": "CI build 1234 finished on branch foo-feature; summarize the test failures",
  "cwd": "/Users/me/code/myproj",
  "ttl_seconds": 3600
}
EOF
```

格式规则：
- 文件 mtime 必须早于"现在 - 1s"（防止 producer 边写边被 tick 读到半截）
- 解析失败 → 文件 rename 到 `triggers/.malformed/<id>.json` + WARN，不阻塞
- `ttl_seconds` 过期 → silently dropped
- 跑完不再保留——目录是 inbox，不是历史

历史在 `~/.seek/cron/runs/<id>.jsonl`，自动 GC（保留 100 个最近 + 30 天最老）。

---

## 6. 故障排查 / Troubleshooting

| 症状 | 大概率原因 | 检查 |
|---|---|---|
| 注册了 job，从来不跑 | OS 调度器没启 | macOS: `launchctl list \| grep seek` · Linux: `systemctl --user list-timers` · Win: `schtasks /query /tn "seek cron tick"` |
| 跑了但每次 auth fail | 子进程 env 没 API key | 检查 `~/.seek/cron/env` 是否存在；`seek cron run <name>` 重现 |
| Linux 跑了但 notify-send 报 "command not found" | libnotify 没装 | `apt install libnotify-bin` / `dnf install libnotify` |
| Windows 启用通知但没弹窗 | v0.6.1 已知限制（见 §4） | 暂时 fallback 到 `seek cron list` 看状态 |
| 同名 job 重复触发 | 上次 run 还没结束，per-job lock 跳过新触发 | 是设计行为，看 `runs/<id>.jsonl` 的 `WARN: prior run still active` |
| 解析 env 文件报错就 spawn fail | **设计如此**——比静默更安全 | 修文件语法；再跑 |
| `tick.lock` held 看到 "skipped" | 上一次 tick 还没退出（应该 < 1 分钟） | 极少见；若每分钟都 skip，看是否上一 tick 死锁 |

CLI 帮助：`seek cron help`。完整 PRD：[`feature-routines.md`](./prd/feature-routines.md)。

---

## 7. 卸载 / Uninstall

```bash
# macOS
launchctl unload ~/Library/LaunchAgents/com.seek.cron.plist
rm ~/Library/LaunchAgents/com.seek.cron.plist

# Linux
systemctl --user disable --now seek-cron.timer
rm ~/.config/systemd/user/seek-cron.{service,timer}
systemctl --user daemon-reload

# Windows
schtasks /delete /tn "seek cron tick" /f

# 然后清 seek 自己的状态（可选）
rm -rf ~/.seek/cron/   # macOS / Linux / WSL
# Win: Remove-Item -Recurse $env:USERPROFILE\.seek\cron
```
