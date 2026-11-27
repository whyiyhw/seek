// web-headless-chrome.mjs — web-*-headless.mjs 全族共用的一次性 Chrome 收尾。
//
// 每个 *-headless.mjs 都用 mkdtempSync 在系统 temp 下建一份 Chrome profile。
// 旧写法只 chrome.kill() 不删目录：每跑一轮在 $TMPDIR 留一份（2026-11-27 实录：
// 全族累计 731 份 / 2.6GB）。收尾不是一句 rmSync，两个坑都实测过：
//   ① 默认的 SIGTERM 让 Chrome 走优雅关闭——目录删完它还在关闭途中把文件写
//      回来（剩 652K）。一次性 profile 不需要体面收场，直接 SIGKILL。
//   ② 主进程死了，辅助进程（存储/网络服务）还要几十毫秒才跟着退，这期间照样
//      落文件，rmSync 撞上就中途报错删一半（剩 620K，时有时无）。'exit' 钩子里
//      只有同步手段：阻塞式小睡重试。
// 详见 docs/pitfalls/web-frontend-asset-plane/ 的「审查脚本每跑一轮漏一份
// Chrome profile」条。零依赖（Node 内建模块），与各脚本同款。
import { rmSync } from 'node:fs';

// 杀掉 Chrome 并删掉它的一次性 profile。给 process.on('exit') 用：必须同步、
// 不抛。删不掉（被占用，Windows 上常见）就留给系统清 temp，只告警不失败——
// 验收结论不受一个临时目录影响。
export function disposeHeadlessChrome(chrome, profile) {
  try { chrome.kill('SIGKILL'); } catch { /* already gone */ }
  if (!profile) return;
  const sleep = new Int32Array(new SharedArrayBuffer(4));
  for (let i = 0; i < 15; i++) {
    try { rmSync(profile, { recursive: true, force: true }); return; } catch { /* 还有进程在写，再试 */ }
    Atomics.wait(sleep, 0, 0, 100);
  }
  console.error(`warn: 一次性 Chrome profile 没删掉，留给系统清 temp：${profile}`);
}
