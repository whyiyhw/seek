### 无头浏览器验收脚本三连坑：await 死亡 Promise 当查活、bash 拼 URL 双斜杠 307、跨块 textContent 无分隔
- **Saw**: W2.1 无头验收脚本（c_5xtm74）三连坑：① 等 DevTools 目标 `await chromeDead`（`new Promise(res => chrome.on('exit', ...))`）永不 resolve，查活变等死；② `BASE` 带尾斜杠拼 `"$BASE/msg"` 得 `//msg`，Go mux 回 307，POST 未送达、SSE curl 拿重定向体——全红无声；③ `m.txt === 期望全文` 永不成立——`<pre>` 块级、`textContent` 块间无分隔，`"A\n"+pre+"B"` 读回 `A\nfuncB`。
- **Why**: ① 永不 resolve 的 Promise 不是布尔，同步语义该用 `chrome.exitCode !== null`；② `http.ServeMux` 对 `//x` 先 301/307 再匹配，curl POST 不跟 307、`curl -sN` 拿重定向体即"完成"；③ `textContent` 纯拼接，块级换行只在源文本里。
- **Fix**: ① `chrome.exitCode !== null`；② `BASE="${BASE%/}"`；③ 断言改 contains（三段+`pres>0`+无 `⚙` 残留）；另加 120s watchdog+每景 trace（首版挂死零输出）。
- **Lesson**: 验收脚本也会卡死：等外部进程要有总闸超时+阶段 trace；URL base 先去尾斜杠（307 静默）；DOM 文本断言只 contains 不 equal。
- **Refs**: `scripts/web-stream-headless.mjs`/`web-stream-manual.sh`、`internal/controlplane/webfrontend.html` renderBody
