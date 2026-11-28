### 「浏览和接续不该是同一个点击」的安全理由，换 dsh 骨架后由懒 resume 保留——别把 /resume 挪回点击事件
- **Saw**: W6 前点会话行=只读浏览器（#browser），再点「继续此会话」才发 /resume。改成 dsh 形态（点会话=进对话）后最顺手的是点行直接 `sendText('/resume '+sid)`——翻一眼历史就换掉进行中的对话，正是 webfrontend.html ~2844 注释要防的事故。
- **Why**: 「看」与「切」成本不对称：看是浏览（零代价、可取消），切是状态变更（/resume 换掉本 conv 绑定，上下文随之丢）；挂在点击上等于让浏览副作用化。
- **Fix**: 懒 resume（W6-5 c_mo7quv）：点行只设 `viewSid` 把历史渲进 #hist（与实时同一渲染器）；真发消息时 `sendText → ensureContext` 先 POST `/resume <sid>`、等 /state 确认生效、再发正文（失败保草稿红字，绝不明发）。新会话同理（`pendingFresh`，第一条消息才 /new 或 /bind）。执行器按 Conv 串行；页面侧两步同一 async 函数按序 await。
- **Lesson**: 交互约束被产品形态推翻时，先找**安全实质**再用新机制保住——危险不在点击而在「无意图即切换」；把切换推迟到**不可逆的发出动作**（第一条消息），形态到了 dsh、约束还在。反过来：以后任何「点行即切」的诱惑都该先看这条。
- **Refs**: `internal/controlplane/webfrontend.html`（`ensureContext`、`viewSession`/`openSession`）、`scripts/web-sessions-headless.mjs`、`docs/notes-web-w6-dsh-shell.md` §0④。
