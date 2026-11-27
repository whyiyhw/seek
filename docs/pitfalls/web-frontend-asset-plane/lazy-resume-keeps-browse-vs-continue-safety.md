### 「浏览和接续不该是同一个点击」的安全理由，换 dsh 骨架后由懒 resume 保留——别把 /resume 挪回点击事件
- **Saw**: W6 之前点会话行 = 进只读浏览器（#browser），要再点「继续此会话」才发 /resume。用户要 dsh 形态（点会话=进入该对话）后，最顺手的改法是点行直接 `sendText('/resume '+sid)`——但这样随便翻一眼历史就会把正在进行的对话换掉，正是 webfrontend.html ~2844 注释里写明的旧约束要防的事故。
- **Why**: 「看」和「切」的成本天然不对称：看是浏览（应零代价、可取消），切是状态变更（/resume 会把本 conv 的绑定换到那条会话，正在进行的对话上下文随之丢）。把切换挂在点击上，等于让浏览副作用化。
- **Fix**: 懒 resume（W6-5 c_mo7quv）：点行只设 `viewSid` 把历史渲进 #hist（与实时同一套渲染器）；真正发消息时 `sendText → ensureContext` 先 POST `/resume <sid>`、等 /state 确认绑定生效、再发正文（失败保草稿红字提示，正文绝不明发）。新会话同理懒执行（`pendingFresh`，第一条消息才 /new 或 /bind）。执行器按 Conv 串行，两条指令的顺序由它保证；页面侧的保证是两步在同一个 async 函数里按序 await。
- **Lesson**: 交互约束被产品形态推翻时，先找它的**安全实质**再用新机制保住它——「一点就切」的危险不在点击而在「无意图即切换」；把切换推迟到**不可逆的发出动作**（第一条消息）上，形态就到了 dsh、约束还在。反过来：以后任何「点行即切」的诱惑都该先看这条。
- **Refs**: `internal/controlplane/webfrontend.html`（`ensureContext`、`viewSession`/`openSession`、工作区树节注释）、`scripts/web-sessions-headless.mjs`（景 L 懒 resume 四则）、`docs/notes-web-w6-dsh-shell.md` §0④。
