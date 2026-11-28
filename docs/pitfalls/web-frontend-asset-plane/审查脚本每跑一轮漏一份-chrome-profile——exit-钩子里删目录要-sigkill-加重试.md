### 审查脚本每跑一轮漏一份 Chrome profile——exit 钩子里删目录要 SIGKILL 加重试

- **Saw**: `$TMPDIR` 积了 75 个 `seek-review-chrome.*`（606MB，最早四天前），每跑一轮 `web-page-review.sh` 多一个。补 `rmSync` 后第一次实测仍漏，且时有时无。
- **Why**: profile 由 `mkdtempSync` 建在系统 temp，脚本从没删。删也不易：`process.on('exit')` 只能做同步事，`chrome.kill()` 默认 SIGTERM——Chrome 优雅关闭，目录删完它还在关途中把 Preferences 写回来；换 SIGKILL 后辅助进程（存储/网络服务）还要几十毫秒才退，期间照样落文件，`rmSync` 撞上就报错、删一半。
- **Fix**: 收尾改 SIGKILL + 阻塞式小睡重试（`Atomics.wait` 100ms × 15，c_zib76h）；成功、失败两路各实测多轮无残留。c_mxura4 抽成 `scripts/web-headless-chrome.mjs`，全族 18 个 `web-*-headless.mjs` 共用。
- **Lesson**: 一次性子进程的收尾别指望优雅信号；在 `exit` 钩子里删它正在写的目录，要「杀硬、等稳、重试」。最小复现（空页跑 4 秒）删得干净，真负载才输——验证要跑真实负载、多轮。修泄漏先 grep 全族（这里是 `mkdtempSync`）：c_zib76h 只修撞见的 page-review，其余 17 个又漏一轮，累计 731 份 / 2.6GB 才被看见。
- **Refs**: `scripts/web-headless-chrome.mjs`（`disposeHeadlessChrome`）、各 `scripts/web-*-headless.mjs` 的 `process.on('exit', …)`
