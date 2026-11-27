### 无头脚本的轮询谓词写成 async 会恒真——断言空过

- **Saw**: web-sessions 景 L 的「发送触发 /resume」「第一条消息触发 /new」几条断言一直绿，独立验证却指出它们没测到验收要求的东西。细看：谓词是 `async () => {…fetch /state…}`，返回的是 Promise。
- **Why**: 该脚本的 `until(what, pred, ms)` 只认同步的快照谓词，循环条件是 `!pred(snapshot)`——Promise 恒为真，第一次迭代就退出，`!!stB` 这类断言拿到的只是一个 Promise 对象。
- **Fix**: 加 `untilEv`（真正 `await pred()`），景 L 改读服务端证据（桩 seek 的调用日志：执行器拉子进程时带 `-resume <sid>`），并做了反向验证——撤掉页面修复，新断言全红（c_mo7quv 返工）。
- **Lesson**: 「await 一个值再判」的谓词不能喂给同步轮询。断言是不是真的，看把被测行为改坏后它红不红——不红的断言等于没有。JS 里 `!!promise` 永远是 true，这类错误不会有任何报错。
- **Refs**: `scripts/web-sessions-headless.mjs`（`until` / `untilEv`、景 L）
