### 工作区组头点击不能发 /bind——/bind 是 start over，导航手势会把正在进行的对话清掉

- **Saw**: 把 dsh 式两级工作区树抄进 web 页时，最自然的实现是「点组头 = 发 `/bind <root>`」（现成的指令、现成的候源）。但那意味着点 A → 点 B → 点回 A 连续丢掉两次上下文：当前会话的 `session_id` 每次都被 `/bind` 的 D-C2 语义（`router.go`「always with a FRESH session」）清空。
- **Why**: 页面里的「导航」与平面里的「绑定」是两个不同的事实。`/bind` 记的是「从这里重新开始」，工作区树要的是「回到我在那儿的地方」——后者在平面上早有一条更准的路径：`/resume <sid>` 会通过 `LookupSession` 把绑定**跟到该会话自己记的 cwd**（`router.go` cmdResume），上下文和项目一起到位。
- **Fix**: `treeBind`（webfrontend.html）按三态分岔：已在该组 → 空操作（什么都不发）；该组有会话 → `/resume` 组内最新一条；空组 → `/bind`（第一次进来本来就无上下文可丢）。同时把 `/resume` 的「未绑定」分支从「回 `/bind` 提示」改成「seam 知道 cwd 就直接用它建绑定」（`cmdResume` + `TestRouter_ResumeUnboundAdoptsSessionCWD`）——否则新页面上点组头永远要先手打一条 `/bind` 才能 resume。
- **Lesson**: 把「选择目标」抄成一条现成指令之前，先读那条指令的**语义名**而不只是它的功能：`/bind` 名字里没有 "bind only"，它做的是 reset。凡是「回到某处」的手势都该走 non-destructive 的那条路径（这里是 `/resume`），并允许它在未绑定时把绑定**补**出来。

**Refs**: `internal/controlplane/webfrontend.html`（工作区树一节与 `treeBind`）· `internal/controlplane/router.go`（`cmdResume`）· `internal/controlplane/router_test.go`（`TestRouter_ResumeUnboundAdoptsSessionCWD`）
