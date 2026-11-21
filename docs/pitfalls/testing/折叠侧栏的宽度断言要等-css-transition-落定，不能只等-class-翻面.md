### 折叠侧栏的宽度断言要等 CSS transition 落定，不能只等 class 翻面
- **Saw**: 无头验收点「⟨」后立刻读 `getBoundingClientRect().width`，`body.side-collapsed` 已为 true 但宽度仍是 260（展开方向同样：class 翻了、宽度还在 44）。
- **Why**: `transition: width .15s` 期间 computed width 连续插值；class 翻面与几何落定是两个时刻。断言写「class 变了就认为收起了」会间歇性红。
- **Fix**: 验收谓词改成 `collapsed && sideW <= 48`（展开 `!collapsed && sideW > 200`）——同一份快照里等两者同时成立。
- **Lesson**: 任何带 transition 的几何断言，等待条件要把**几何量**写进谓词，别只等状态位。
- **Refs**: `scripts/web-sessions-headless.mjs` 景 5；`internal/controlplane/webfrontend.html` `#side { transition: width .15s; }`。
