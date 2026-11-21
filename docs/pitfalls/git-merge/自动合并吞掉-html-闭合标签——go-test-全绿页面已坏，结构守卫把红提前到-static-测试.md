### 自动合并吞掉 HTML 闭合标签——go test 全绿页面已坏，结构守卫把红提前到 static 测试
- **Saw**: 2026-11-21 一天两例合并语义冲突。af1be012（main 并入 drive/c_7lsngm）：auto-merge 在**非冲突区**静默吞掉 `<section id="settings">` 的 `</section>`，form+tray 被圈进 display:none 面板（0×0、全文档拒焦），`go test` 96 包全绿，靠无头验收才发现；同日 5b57c1ba：合并丢 sync import（编译期炸，gate 兜得住——HTML 没有这个兜底）。
- **Why**: 三方合并是文本级配对，冲突块外的静默取舍不报任何冲突；编译器只看得见 Go——内嵌 webfrontend.html 是纯数据资产，没有任何工具链解析它。drive/* 高频预并全靠文本级 auto-merge，卡片并发度越高，「合并干净、语义已坏」越多。
- **Fix**: c_lxe3lz 在 webfrontend_static_test.go 加结构守卫三组断言（好绿/构造坏红两态齐全，坏样本全部由 webPage 字符串变换构造、无 fixture）：①section/form/script/div 开闭计数配平；②所有 id="…" 全页唯一；③id="form"/"input"/"menu"/"send" 各恰一次且 textarea#input 在场（输入面被吞直接红）。失败信息一律带合并提示指路本条。
- **Lesson**: 「自动合并干净不算完」族的内嵌资产版：hash 钉（webPageSHA256）只逼你自觉改钉、不告诉你坏在哪；整块吞掉一个配对元素时开闭计数仍相等——恰一次断言与配平断言缺一不可。非编译资产的不变式要自己长在测试层，把「页面被无声打坏」从无头验收提前到 go test 红。
- **Refs**: `internal/controlplane/webfrontend_static_test.go`（TestWeb_PageStructureGuard 族）；判例 af1be012、5b57c1ba；同族：git-merge/同一插入点的双方增量…、web-frontend-asset-plane/主题卡晚于别的卡合 main…。
