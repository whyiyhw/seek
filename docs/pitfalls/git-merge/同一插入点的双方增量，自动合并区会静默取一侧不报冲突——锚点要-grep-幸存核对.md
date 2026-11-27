### 同一插入点的双方增量，自动合并区会静默取一侧不报冲突——锚点要 grep 幸存核对
- **Saw**: 卡 c_4doebx merge main 后，本卡新增的 `<div id="ag-fields">` 在非冲突区消失（4 个显式冲突文件之外）；main 的 W5 布局重排把 ag-detail 区整块重写（容器加深缩进），本卡基于旧上下文的小插入被静默丢弃，零冲突标记；同文件更远处的 JS 增量完好幸存。
- **Why**: 三方合并按文本上下文配对：一方整块重写、另一方在旧块中间插入时，插入 hunk 的上下文在新版里没有锚点，git 采信重写侧。
- **Fix**: merge 解完显式冲突 ≠ 完工：对本卡的结构性锚点清单（新增 id/类名/函数名/常量，如 `ag-fields`/`st-running`/`syncAgentRefresh`/`AG_REFRESH_MS`）跑 `grep -c` 幸存核对，缺哪个补哪个再 commit。
- **Lesson**: 「冲突文件数」只圈显式冲突；双方都动过的自动合并区里，重写方会静默吃掉插入方，验收靠锚点 grep 不靠 git 报 "Automatic merge went well"。
- **Refs**: `internal/controlplane/webfrontend.html` ag-detail 区；card c_4doebx 重派轮。同家族（文本不冲突、语义冲突）：上一条「编号小节撞号」。
