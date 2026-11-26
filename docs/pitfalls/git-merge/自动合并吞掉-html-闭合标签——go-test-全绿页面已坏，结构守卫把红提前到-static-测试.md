### 自动合并吞掉 HTML 闭合标签——go test 全绿页面已坏，结构守卫把红提前到 static 测试
- **Saw**: 2026-11-21 两例：af1be012（main 并入 drive/c_7lsngm）在非冲突区静默吞掉 `<section id="settings">` 的 `</section>`，form+tray 被圈进 display:none 面板（0×0、拒焦），`go test` 96 包全绿，靠无头验收才发现；5b57c1ba 丢 sync import（编译期炸；HTML 无此兜底）。
- **Why**: 三方合并按文本配对，冲突块外的取舍不报冲突；编译器只见 Go，内嵌 webfrontend.html 是纯数据资产；drive/* 高频预并放大风险。
- **Fix**: c_lxe3lz 在 webfrontend_static_test.go 加结构守卫：①section/form/script/div 开闭配平；②id="…" 全页唯一；③id="form"/"input"/"menu"/"send" 各恰一次且 textarea#input 在场；失败信息带合并提示。
- **Lesson**: hash 钉（webPageSHA256）只逼你改钉、不说哪坏；整块吞掉配对元素时计数仍相等——恰一次与配平断言缺一不可。非编译资产的不变量要长在测试层。
- **Refs**: `internal/controlplane/webfrontend_static_test.go`（TestWeb_PageStructureGuard）；判例 af1be012、5b57c1ba；同族：git-merge/同一插入点的双方增量…、web-frontend-asset-plane/主题卡晚于别的卡合 main…。
