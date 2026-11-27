#!/usr/bin/env node
// web-bindpick-headless.mjs — /bind、/resume 指令面的无头浏览器验收
//（scripts/web-bindpick-manual.sh 的驱动）。
//
// 本族原名「项目候选点选（c_lnsdvu）」：那时顶栏有 /bind、/projects 两个按钮，
// 点开出项目候选弹层（#menu 的第三个候源），点行或 ↑↓+Enter 即发 /bind。W5 的
// 6e5be518 把这套弹层连同 openBindPicker、两个按钮一起退役（左栏两级工作区树
// 取代），脚本却还在点 #cmd-bind——弹层相关的景自此在 main 上常红（2026-11-27
// W6-4 重验实录：4 条超时）。测已退役功能的断言没有守护价值，只剩噪音：这里
// 只留页面还在的指令面——
//   景 0   → 基线：#menu 初始关
//   景 1   → 手打 /bind alpha + /resume sid → /state 会话 = sid（绑定 + 续接链）
//   景 2   → 手打 /bind <不存在路径> → 聊天面「绑定失败：目录不存在：…」，
//            且绑定不动（Router 应答如实透传，页面不预检）
// 服务端契约（project/list、settings/get、/projects 文本回复、失败应答）由
// manual.sh 的 curl 自检守着，不在这里重复。目录浏览器（#ws-chip 打开的
// openDirPicker）是另一套 UI，归 web-dirpick 族。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动
// 本机 headless Chrome。输入框赋值后派 input 事件 + 点 #send 提交（表单 submit 链）。
//
// 用法：node web-bindpick-headless.mjs <页面URL> <projA> <sid> [--gone <不存在路径>]
//       [--chrome <chrome 可执行文件>]
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { disposeHeadlessChrome } from './web-headless-chrome.mjs';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const flag = (name, dflt) => {
  const i = argv.indexOf(name);
  const v = i > 0 ? argv[i + 1] : undefined;
  return v === undefined ? dflt : v;
};
const CHROME = flag('--chrome', process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
const GONE = flag('--gone', '/tmp/seek-bp-gone');
const pos = argv.filter((a, i) => !a.startsWith('--') && argv[i - 1] !== '--chrome'
  && argv[i - 1] !== '--gone');
const pageUrl = pos[0];
const PROJ_A = pos[1], SID = pos[2];
if (!pageUrl || !PROJ_A || !SID) {
  console.error('usage: web-bindpick-headless.mjs <pageUrl> <projA> <sid> [--gone p] [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const WATCHDOG_MS = 120_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18785;   // 与 web-rename(18796) 等错开
const profile = mkdtempSync(path.join(tmpdir(), 'seek-bp-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', '--window-size=1440,900',
  `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动，等 DevTools 目标`);
const chromeErr = [];
chrome.stderr.on('data', d => chromeErr.push(d));
process.on('exit', () => disposeHeadlessChrome(chrome, profile));   // 杀 Chrome + 删一次性 profile
process.on('SIGTERM', () => process.exit(1));
process.on('SIGINT', () => process.exit(1));

let target;
for (let i = 0; i < 50; i++) {
  try {
    const r = await fetch(`http://127.0.0.1:${dbgPort}/json/list`);
    const list = await r.json();
    target = list.find(t => t.type === 'page' && t.url.startsWith('http'));
    if (target) break;
  } catch { /* not up yet */ }
  await delay(200);
}
if (!target) {
  console.error('FAIL: 10s 内未抓到 DevTools 目标：\n'
    + chromeErr.join('').split('\n').slice(-5).join('\n'));
  process.exit(1);
}
trace('目标已连：' + target.url);

// ---- 2. CDP：最小 Runtime.evaluate 客户端 ----
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0;
const pending = new Map();
ws.onmessage = e => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) {
    const { res, rej } = pending.get(m.id);
    pending.delete(m.id);
    m.error ? rej(new Error(m.error.message)) : res(m.result);
  }
};
async function evalJS(expr) {
  const r = await new Promise((res, rej) => {
    const id = ++seq;
    pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method: 'Runtime.evaluate',
      params: { expression: expr, awaitPromise: true, returnByValue: true } }));
  });
  if (r.exceptionDetails) {
    throw new Error('page eval failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  }
  return r.result.value;
}

// 页面快照：弹层/状态行/聊天面，全部断言读这一份。
const snapshot = () => evalJS(`(() => ({
  menuHidden: document.getElementById('menu').hidden,
  status: document.getElementById('status').textContent,
  chat: document.getElementById('list').innerText,
  inputVal: document.getElementById('input').value,
}))()`);
// /state：页面同源 fetch（cookie 已落），conv 身份从 localStorage 取——
// 与页面自身 refreshState 同一条链。
const state = () => evalJS(`fetch('/state', {headers: {'X-Seek-Conversation':
  localStorage.getItem('seek-web-conv') || ''}}).then(r => r.json())`);
const typeAndSend = text => evalJS(`(() => {
  const el = document.getElementById('input');
  el.value = ${JSON.stringify(text)};
  el.dispatchEvent(new Event('input', { bubbles: true }));
  document.getElementById('send').click();
  return 'ok';
})()`);

const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snapshot(), null, 1)); } catch { /* 页面已关 */ } };
async function until(what, pred, ms) {
  const t0 = Date.now();
  let s = await snapshot();
  while (!(await pred(s))) {   // await：pred 可为 async（如内嵌 /state 轮询）
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(100);
    s = await snapshot();
  }
  return s;
}

// 就绪：/state 探测落定（?token= 首跳已被 303 换 cookie）。
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = '';
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
  } catch { /* 页面未就绪，继续轮询 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}

// 景 0：基线 —— #menu（'/'、'@' 候选弹层）初始关。
trace('景 0：基线');
try {
  const s = await snapshot();
  ck('基线：弹层初始关', s.menuHidden, '');
} catch (e) { ck('基线', false, e.message); await dump(); }

// 景 1：手打 /bind alpha、/resume 夹具会话 → /state 会话 = sid。
trace('景 1：手打 /bind + /resume');
try {
  await typeAndSend('/bind ' + PROJ_A);
  await until('已绑定 alpha', s => s.chat.includes('已绑定项目'), 8000);
  await typeAndSend('/resume ' + SID);
  await until('已续接会话', s => s.chat.includes('已续接会话'), 8000);
  const st = await state();
  ck('手打 /bind + /resume → /state 会话 = 夹具 sid',
    st.bound === true && st.project === PROJ_A && st.session === SID,
    JSON.stringify({ bound: st.bound, project: st.project, session: st.session }));
} catch (e) { ck('手打链', false, e.message); await dump(); }

// 景 2：手打不存在路径 → Router 的失败应答如实透传到聊天面（页面不预检），
// 且绑定不动。
trace('景 2：不存在路径');
try {
  await typeAndSend('/bind ' + GONE);
  const s = await until('绑定失败应答', s => s.chat.includes('绑定失败：目录不存在'), 8000);
  ck('手打不存在路径 → 「绑定失败：目录不存在：…」', s.chat.includes('绑定失败：目录不存在：' + GONE), '');
  const st = await state();
  ck('失败应答后绑定不动（仍是 alpha）', st.project === PROJ_A, String(st.project));
} catch (e) { ck('不存在路径', false, e.message); await dump(); }

console.log(fails.length ? `FAIL：${fails.length} 项未过` : '全部断言过');
process.exit(fails.length ? 1 : 0);
