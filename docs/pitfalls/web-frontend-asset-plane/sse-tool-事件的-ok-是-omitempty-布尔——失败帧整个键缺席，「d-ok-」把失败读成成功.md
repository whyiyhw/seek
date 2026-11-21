### SSE tool 事件的 ok 是 omitempty 布尔——失败帧整个键缺席，「d.ok?」把失败读成成功
- **Saw**: c_ux7lvk 调用树验收：桩 seek 吐 `tool_end error:"exit 1"`，页面却渲染 `✔ bash`（无 bad 态）——旧代码 `d.ok ? '✔ ' : '✘ '` 与 `!d.ok` 的读法自 W2.1 起就把每一帧失败都显示成成功（仅 .bad 类碰巧由 `!d.ok` 点亮，图标与文案互相矛盾）。
- **Why**: `webToolEvent.OK` 是 `json:"ok,omitempty"`——Go 的 omitempty 对布尔零值整键不发，wire 上只有 `ok:true`（成功）与「键不在」（失败）两态；JS 侧 `undefined` 在 truthy 判断里归入成功侧。
- **Fix**: 判定改为显式 `d.ok === true`（end 帧 true 才算成功），trowPaint/toolPlainRow 两路同口径；commit 带 c_ux7lvk。
- **Lesson**: 消费 omitempty 布尔时，三态压成两态的合流方向在「读的人」手里——凡是「只在 true 时出现」的键，判定必须写 `=== true`，truthy 读法永远把缺席帧读成反面语义的反面。给 wire 布尔加 omitempty 时，注释里要写明「缺席=假」的消费口径。
- **Refs**: `internal/controlplane/webfrontend_stream.go` `webToolEvent`；`internal/controlplane/webfrontend.html` `toolTreeRow`/`toolPlainRow`。
