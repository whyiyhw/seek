### placeholder 不计入 textarea 自增高——长文案在窄屏折成第二行，被 rows=1 的 24px 内容高整行裁掉
- **Saw**: 390px 已绑定态 #input 的占位「直接发消息；/ 看指令，@ 引用历史会话」折成两行，第二行「@ 引用历史会话」只露上半截（11-26 版）或整行不见（W6-8 后），亮暗一致，02/04/12/26 四镜全中。
- **Why**: ≤640px 的 `#input { font-size: 16px }`（防 iOS 聚焦缩放）把 283px 宽的文案塞进 260px 内容宽（卡 292 − 左右 padding 32）；`grow()` 按 `scrollHeight` 长高，而 **textarea 的 scrollHeight 不含 placeholder**，rows=1 时 contentH 恰好 24px = 一行 → 第二行被 overflow 裁掉。桌面 740px 内容宽一行放得下，所以只有手机宽中招。
- **Fix**: ≤640px 换一行放得下的短占位（`PLACE_NARROW = '发消息，/ 看指令'`，实测 120.75px），宽屏保留长文案；切换点只有 `applyWsChip()` 一处，另挂 `matchMedia('(max-width: 640px)').addEventListener('change')` 管转屏。静态断言 `TestWeb_PageComposerMobilePlaceholder` 钉两条文案 + 单一三元 + 短文案 ≤12 字且保住 `/`；`webPageSHA256` 同 commit 更新。
- **Lesson**: 「输入框里放长提示」先量 textW vs contentW，**placeholder 的高度不参与任何自动增高**——它只借用首行空间，第二行必被裁；窄屏的长提示要么换短文案，要么提到输入框外（提及入口的常驻提示，别塞进占位）。
- **Refs**: `internal/controlplane/webfrontend.html` `applyWsChip()` / `#input` 与 `@media (max-width: 640px)`；`internal/controlplane/webfrontend_test.go` `TestWeb_PageComposerMobilePlaceholder`；复核脚本（只落 out/，不入库）`out/probe-xhwykc/probe.mjs`（对照组：同一框量旧长文案仍报 2 行/露 1 行）。
