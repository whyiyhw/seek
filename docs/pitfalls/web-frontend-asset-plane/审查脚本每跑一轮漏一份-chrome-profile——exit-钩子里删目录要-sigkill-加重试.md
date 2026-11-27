### 审查脚本每跑一轮漏一份 Chrome profile——exit 钩子里删目录要 SIGKILL 加重试

- **Saw**: `$TMPDIR` 里积了 75 个 `seek-review-chrome.*`（606MB，最早四天前）；每跑一轮 `web-page-review.sh` 多一个。补上 `rmSync` 之后第一次实测仍然漏，而且时有时无。
- **Why**: profile 由 `mkdtempSync` 建在系统 temp 下，脚本从来没有删。删也不容易：`process.on('exit')` 钩子只能做同步事，而 `chrome.kill()` 默认 SIGTERM——Chrome 走优雅关闭，目录删完它还在关闭途中把 Preferences 写回来；换成 SIGKILL 后，辅助进程（存储/网络服务）还要几十毫秒才跟着退，期间照样落文件，`rmSync` 撞上就中途报错、删一半。
- **Fix**: 收尾改 SIGKILL + 阻塞式小睡重试（`Atomics.wait` 100ms × 15，c_zib76h）；成功、失败两条路径各实测多轮无残留。c_mxura4 把这段抽成 `scripts/web-headless-chrome.mjs`，全族 18 个 `web-*-headless.mjs` 共用。
- **Lesson**: 一次性子进程的收尾别指望优雅信号；在 `exit` 钩子里清理它正在写的目录，要「杀硬、等稳、重试」。最小复现（空页跑 4 秒）删得很干净，真负载下才会输——验证清理逻辑要跑真实负载、多跑几轮。修泄漏先 grep 全族（这里是 `mkdtempSync`）：c_zib76h 只修了撞见的 page-review，其余 17 个原样又漏了一轮，累计到 731 份 / 2.6GB 才被看见。
- **Refs**: `scripts/web-headless-chrome.mjs`（`disposeHeadlessChrome`）、各 `scripts/web-*-headless.mjs` 的 `process.on('exit', …)`
