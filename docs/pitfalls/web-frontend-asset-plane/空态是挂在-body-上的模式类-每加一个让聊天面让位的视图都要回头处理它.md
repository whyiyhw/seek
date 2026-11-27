### 空态是挂在 body 上的模式类——每加一个「让聊天面让位」的视图，都得回头处理它

- **Saw**: W6-4 的空态（Hero + composer 作为一组纵向居中，`body.chat-empty-on`）落地后会话头接连三次压错位置：先是绝对定位的会话头横穿侧栏、盖住品牌行；修完是空态下打开后台面板仍按空态布局排；再修完，未绑定的空态点开侧栏一条历史，会话头又盖在只读浏览器自己的头行（`#browser-head`）上。三次 go test 都是绿的，都是量几何/看截图才发现。
- **Why**: 空态判据只看实时列表（`#list` 里有没有消息），这没错。问题在 `chat-empty-on` 是挂在 body 上的布局模式，它改的是共享骨架（`#center` 的纵向对齐、`#conv-head` 的定位）；而「让聊天面让位」的视图是另一组正交的 body 类（面板 `browsing`、只读浏览器 `sess-detail`、W6-5 起的看历史 `viewing`）。每加一个视图就是一个新的状态组合——组合没有清单，所以每加一次漏一次。W6-5 返工时同一组「让位」规则还被打包删过一回（f5092ebb 找回）。
- **Fix**: 每个让位视图显式写回内容态：`body.<视图>.chat-empty-on #center { justify-content: flex-start; }` + `#conv-head { position: static; }`。量测脚本 `scripts/web-composer-headless.mjs` 按同一张视图清单逐态量会话头（在主栏内、在文档流、不压下一个头行）；本态修复前该量测 2 项红。
- **Lesson**: 往 body 上挂一个会改共享骨架的模式类之前，先 grep `classList.add` 把「会让主区换面」的 body 类全列出来，逐个组合看一眼；验收探针按同一张清单枚举，别只量正在修的那一态。Go 里的字符串钉只钉得住「规则在不在」，钉不住「还缺哪一态」。
- **Refs**: `internal/controlplane/webfrontend.html`（`body.*.chat-empty-on` 规则组，c_ljtmy4）、`scripts/web-composer-headless.mjs`、`docs/pitfalls/web-frontend-asset-plane/渲染载体换了类名，截图驱动的就绪谓词没跟——等到超时硬拍，nav-errors-没人看.md`
