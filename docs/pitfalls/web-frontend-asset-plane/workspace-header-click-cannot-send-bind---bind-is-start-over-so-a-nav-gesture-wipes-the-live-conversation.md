### 工作区组头点击不能发 /bind——/bind 是 start over，导航手势会把正在进行的对话清掉
- **Saw**: dsh 式两级工作区树抄进 web 页，最自然是「点组头=发 `/bind <root>`」，但点 A→B→回 A 连丢两次上下文：`session_id` 每次被 `/bind` 的 D-C2 语义（`router.go`「always with a FRESH session」）清空。
- **Why**: `/bind` 记「从这里重新开始」，工作区树要「回到我在那儿的地方」——`/resume <sid>` 经 `LookupSession` 把绑定跟到该会话自己记的 cwd（`router.go` cmdResume）。
- **Fix**: `treeBind`（webfrontend.html）三态：已在该组→空操作；有会话→`/resume` 组内最新；空组→`/bind`。`/resume` 未绑定分支从「回 `/bind` 提示」改为「seam 知道 cwd 直接建绑定」（`cmdResume`+`TestRouter_ResumeUnboundAdoptsSessionCWD`）。
- **Lesson**: 把「选择目标」抄成现成指令前先读语义名：`/bind` 做的是 reset；「回到某处」手势走 non-destructive 路径（`/resume`），允许未绑定时补出绑定。
- **Refs**: `internal/controlplane/webfrontend.html` · `internal/controlplane/router.go` · `internal/controlplane/router_test.go`
