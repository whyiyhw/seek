#!/usr/bin/env node
// web-approval-headless.mjs — W5 卡 c_n37tnr（远程审批卡片）的无头浏览器
// 验收（scripts/web-approval-manual.sh 的驱动）。零依赖：Node ≥22（全局
// fetch + WebSocket）经 CDP 驱动本机 headless Chrome；消息用页面自己的
// fetch(q('/msg')) 发出（与真人同线），决定走页面的 rpc()（同源 /rpc）。
//
// 断言（卡面验收的浏览器半边）：
//   ③ 空态零侵入：启动后角标 hidden、卡栈空（:empty 隐藏）
//   ① 全流转：发「审批」→ 卡弹出（描述+命令摘要+三按钮）+ 角标「待审批 1」
//      → 点「允许」（check sprite + 文字）→ 按钮同步禁用（防抖）→ 卡消失、角标隐没
//   ② 幂等：再触发一卡 → 用页面 rpc() 连发两次 decide → 第一次
//      applied=true、第二次 applied=false（决定不双发），回执撤卡
//
// 用法：node scripts/web-approval-headless.mjs <页面URL> <桩项目路径>
//       [--chrome <chrome 可执行文件>]（默认 CHROME env 或 macOS 标准位）
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { disposeHeadlessChrome } from './web-headless-chrome.mjs';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const chromeIdx = argv.indexOf('--chrome');
const CHROME = chromeIdx > 0 ? argv[chromeIdx + 1]
  : process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const args = argv.filter((a, i) => a !== '--chrome' && i !== chromeIdx + 1);
const pageUrl = args[0];
const projDir = args[1];
if (!pageUrl || !projDir) {
  console.error('usage: web-approval-headless.mjs <pageUrl> <projDir> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const WATCHDOG_MS = 120_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. headless Chrome + CDP（web-stream-headless.mjs 同款骨架）----
const dbgPort = 18794;
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5ap-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动，等 DevTools 目标（${pageUrl}）`);
const chromeErr = [];
chrome.stderr.on('data', d => chromeErr.push(d));
process.on('exit', () => disposeHeadlessChrome(chrome, profile));   // 杀 Chrome + 删一次性 profile
process.on('SIGTERM', () => process.exit(1));
process.on('SIGINT', () => process.exit(1));

let target = null;
for (let i = 0; i < 100 && !target; i++) {
  await delay(200);
  if (chrome.exitCode !== null) break;
  try {
    const list = await (await fetch(`http://127.0.0.1:${dbgPort}/json/list`,
      { signal: AbortSignal.timeout(2000) })).json();
    target = list.find(t => t.type === 'page' && t.webSocketDebuggerUrl
      && !t.url.startsWith('devtools'));
  } catch { /* DevTools 未起，继续轮询 */ }
}
if (!target) {
  console.error('FAIL: 20s 内未见 Chrome 调试目标；stderr 尾部：\n'
    + chromeErr.join('').split('\n').slice(-5).join('\n'));
  process.exit(1);
}
trace('目标已连：' + target.url);

const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
trace('CDP WebSocket 已开');
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
const send = text => evalJS(
  `fetch(q('/msg'), { method: 'POST',
     headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
     body: JSON.stringify({ text: ${JSON.stringify(text)} }) }).then(r => r.status)`);
const apSnap = () => evalJS(`(() => ({
  badgeHidden: document.getElementById('ap-badge').hidden,
  badgeText: document.getElementById('ap-badge').textContent,
  stackShown: getComputedStyle(document.getElementById('approvals')).display !== 'none',
  cards: [...document.querySelectorAll('.apcard')].map(c => ({
    id: c.dataset.id,
    txt: c.textContent,
    cmd: c.querySelector('pre') ? c.querySelector('pre').textContent : '',
    nbtn: c.querySelectorAll('button').length,
    btnDisabled: [...c.querySelectorAll('button')].map(b => b.disabled),
  })),
}))()`);
const clickBtn = (needle, idx) => evalJS(
  `(() => { const bs = [...document.querySelectorAll('.apcard button')]
     .filter(b => b.textContent.includes(${JSON.stringify(needle)}));
     const b = bs[${idx ?? 0}]; if (!b) return false; b.click(); return true; })()`);

// ---- 2. 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await apSnap(), null, 1)); } catch { /* 页面已关 */ } };
async function until(what, pred, ms) {
  const t0 = Date.now();
  let s = await apSnap();
  while (!pred(s)) {
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(120);
    s = await apSnap();
  }
  return s;
}

trace('等页面就绪');
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = '';
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
  } catch { /* 页面脚本未到，继续轮询 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}

// ③ 空态零侵入：未触发任何审批时，角标 hidden、卡栈空。
trace('③ 空态');
{
  const s = await apSnap();
  ck('③ 空态：角标 hidden', s.badgeHidden === true, JSON.stringify(s));
  ck('③ 空态：卡栈空且隐藏', s.cards.length === 0 && s.stackShown === false, JSON.stringify(s));
}

trace('景 0：/bind');
if ((await send(`/bind ${projDir}`)) !== 202) { console.error('FAIL: /bind 非 202'); await dump(); process.exit(1); }
{
  let bound = false;
  for (let i = 0; i < 60 && !bound; i++) {
    await delay(150);
    bound = await evalJS(`document.getElementById('status-l').textContent.includes('已绑定')`);
  }
  ck('/bind 落定（顶栏已绑定）', bound === true, '9s 内未见已绑定状态行');
}

// ① 全流转：审批 → 卡片+角标 → 允许 → 防抖禁用 → 撤卡。
trace('① 全流转：弹卡');
try {
  if ((await send('审批')) !== 202) throw new Error('POST 非 202');
  const s = await until('卡片弹出', s => s.cards.length === 1, 10000);
  const card = s.cards[0];
  ck('① 卡片：描述+命令摘要+三按钮',
    card.txt.includes('在桩项目里跑一次全量测试')
      && card.cmd === 'go test ./... -count=1' && card.nbtn === 3,
    JSON.stringify(card));
  ck('① 角标：待审批 1 常显', s.badgeHidden === false && s.badgeText.includes('待审批') && s.badgeText.includes('1'), JSON.stringify(s));
  ck('① 卡片 id 进 dataset（幂等键）', /^ap-e2e-/.test(card.id), card.id);

  ck('① 点击「允许」（按钮真在）', (await clickBtn('允许')) === true, '');
  const dis = await until('按钮禁用（防抖）', s => s.cards.length === 0
    || s.cards[0].btnDisabled.every(Boolean), 3000);
  ck('① 防抖：点击后按钮同步禁用',
    dis.cards.length === 0 || dis.cards[0].btnDisabled.every(Boolean), JSON.stringify(dis));
  const fin = await until('卡片消失+角标隐没',
    s => s.cards.length === 0 && s.badgeHidden === true, 8000);
  ck('① 撤卡：卡片消失', fin.cards.length === 0, '');
  ck('① 撤卡：角标回到 hidden（空态零侵入回归）', fin.badgeHidden === true, '');
} catch (e) { ck('① 全流转场景', false, e.message); await dump(); }

// ② 幂等：再触发一卡，页面 rpc() 连发两次同决定——第二次 applied=false，
// 且回执（SSE resolved）把卡撤下。
trace('② 幂等：双发 decide');
try {
  if ((await send('审批')) !== 202) throw new Error('POST 非 202');
  const s = await until('第二张卡弹出', s => s.cards.length === 1, 10000);
  const id = s.cards[0].id;
  const two = await evalJS(`(async () => {
    const a = await rpc('approval/decide', { id: ${JSON.stringify(id)}, decision: 'n' });
    const b = await rpc('approval/decide', { id: ${JSON.stringify(id)}, decision: 'n' });
    return [a, b];
  })()`);
  ck('② 双发：第一次 applied=true', two[0].applied === true && two[0].decision === 'n',
    JSON.stringify(two));
  ck('② 双发：第二次 applied=false（决定不双发）', two[1].applied === false && two[1].decision === 'n',
    JSON.stringify(two));
  const fin = await until('回执撤卡', s => s.cards.length === 0 && s.badgeHidden === true, 8000);
  ck('② 回执撤卡：卡片消失+角标隐没', fin.cards.length === 0 && fin.badgeHidden === true, '');
} catch (e) { ck('② 幂等场景', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（W5 c_n37tnr 无头验收）');
process.exit(fails.length ? 1 : 0);
