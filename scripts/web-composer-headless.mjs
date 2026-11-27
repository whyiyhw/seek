#!/usr/bin/env node
// web-composer-headless.mjs — W6-4 composer 卡片 + 主区几何的无头量测
// （scripts/web-composer-manual.sh 的驱动；验收②从一次性探针落成脚本）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动本机
// headless Chrome，只量不点（点也只点面板钮/会话行这类视图开关）。量四组：
//   ① 空态：composer 卡片中心 y ∈ [380,520]、宽 774±2、左右边距差 ≤2px；
//   ② 有消息：卡片底距视口底 ≤40px；
//   ③ 会话头只在主栏里（左缘 ≥ 侧栏右缘）——空态、看历史、面板打开各态都量；
//   ④ 面板打开时聊天面整体让位（#list / #hist / form / #composer-meta）。
// 看历史一态（#hist，W6-5 起把历史渲进主转录区）量：会话头在文档流、输入卡片
// 可见且贴底。只读浏览器已随 W6-5 退役，这里不再有它的景。
//
// 用法：web-composer-headless.mjs <pageUrl> <projDir> <sid> [--chrome path] [--dbg-port N]
// 退出码：0 全过；1 有不过的量测（stderr 不吐 DOM，stdout 每条 ok/FAIL 带实测值）。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { disposeHeadlessChrome } from './web-headless-chrome.mjs';

const argv = process.argv.slice(2);
const opt = (name, dflt) => {
  const i = argv.indexOf(name);
  return i > 0 && i + 1 < argv.length ? argv[i + 1] : dflt;
};
const CHROME = opt('--chrome', process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
const DBG_PORT = parseInt(opt('--dbg-port', '28050'), 10);
const posArgs = argv.filter((a, i) => !a.startsWith('--') && argv[i - 1] !== '--chrome' && argv[i - 1] !== '--dbg-port');
const [pageUrl, projDir, sid] = posArgs;
if (!pageUrl || !projDir || !sid) {
  console.error('usage: web-composer-headless.mjs <pageUrl> <projDir> <sid> [--chrome path] [--dbg-port N]');
  process.exit(1);
}

const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};

const profile = mkdtempSync(path.join(tmpdir(), 'seek-w6composer-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${DBG_PORT}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
process.on('exit', () => disposeHeadlessChrome(chrome, profile));

let target = null;
for (let i = 0; i < 100 && !target; i++) {
  await delay(200);
  try {
    const list = await (await fetch(`http://127.0.0.1:${DBG_PORT}/json/list`,
      { signal: AbortSignal.timeout(2000) })).json();
    target = list.find(t => t.type === 'page' && t.webSocketDebuggerUrl
      && !t.url.startsWith('devtools'));
  } catch { /* DevTools 未起，继续轮询 */ }
}
if (!target) { console.error('FAIL: 20s 内未见 Chrome 调试目标'); process.exit(1); }

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
const call = (method, params = {}) => new Promise((res, rej) => {
  const id = ++seq;
  pending.set(id, { res, rej });
  ws.send(JSON.stringify({ id, method, params }));
});
async function evalJS(expr) {
  const r = await call('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) throw new Error('page eval failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  return r.result.value;
}
const delay_ = ms => new Promise(r => setTimeout(r, ms));
async function until(what, pred, ms = 8000) {
  const t0 = Date.now();
  for (;;) {
    let v = null;
    try { v = await pred(); } catch { /* 渲染中途的瞬时态 */ }
    if (v) return v;
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay_(120);
  }
}
const send = text => evalJS(`fetch(q('/msg'), { method: 'POST',
  headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
  body: JSON.stringify({ text: ${JSON.stringify(text)} }) }).then(r => r.status)`);
const status = `document.getElementById('status').textContent`;
async function waitReady() {
  for (let i = 0; i < 100; i++) {
    await delay(150);
    let ok = false;
    try { ok = await evalJS(`!${status}.includes('连接中')`); } catch { /* 导航中 */ }
    if (ok) return;
  }
  throw new Error('页面 15s 未就绪');
}

// 几何快照：一次求值取全部，断言只读这一份。
const geo = () => evalJS(`(() => {
  const r = el => { if (!el) return null; const b = el.getBoundingClientRect();
    return { x: b.x, y: b.y, w: b.width, h: b.height }; };
  const shown = el => !!el && getComputedStyle(el).display !== 'none';
  const c = document.getElementById('center');
  const f = document.querySelector('#center > form');
  const form = r(f), center = r(c), side = r(document.getElementById('side')), head = r(document.getElementById('conv-head'));
  return {
    cls: document.body.className,
    empty: !!document.querySelector('#list .chat-empty'),
    nmsgs: document.querySelectorAll('#list .msg').length,
    form: form && { x: form.x, w: form.w, cy: form.y + form.h / 2, bottomGap: innerHeight - form.y - form.h,
      lGap: form.x - center.x, rGap: (center.x + center.w) - (form.x + form.w) },
    sideRight: side.x + side.w, headX: head.x,
    headPos: getComputedStyle(document.getElementById('conv-head')).position,
    hasHist: !!document.getElementById('hist'),
    shown: { list: shown(document.getElementById('list')), hist: shown(document.getElementById('hist')),
      form: shown(f), meta: shown(document.getElementById('composer-meta')), pane: shown(document.getElementById('agents')) },
  }; })()`);
const inMain = g => g.headX >= g.sideRight - 0.5;

await call('Runtime.enable');
await waitReady();
await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
await evalJS('setSideCollapsed(false)');
// 侧栏宽度有 .15s 过渡：等它落到 280 再量（动画中途的读数不是任何一态）。
await until('侧栏落定', () => evalJS(`Math.abs(document.getElementById('side').getBoundingClientRect().width - 280) <= 1`), 5000);

try {
  // ① 空态（刚打开、未绑定）
  await until('空态 Hero 在场', async () => (await geo()).empty, 8000);
  let g = await geo();
  ck('空态 composer 中心 y ∈ [380,520]', g.form.cy >= 380 && g.form.cy <= 520, `cy=${g.form.cy.toFixed(1)}`);
  ck('空态 composer 宽 774±2', Math.abs(g.form.w - 774) <= 2, `w=${g.form.w}`);
  ck('空态 composer 水平居中（左右边距差 ≤2px）', Math.abs(g.form.lGap - g.form.rGap) <= 2,
    `lGap=${g.form.lGap} rGap=${g.form.rGap}`);
  ck('空态会话头在主栏内（左缘 ≥ 侧栏右缘）', inMain(g), `headX=${g.headX} sideRight=${g.sideRight} pos=${g.headPos}`);

  // 空态 + 面板打开（实时列表为空时进面板）：最容易漏的组合——空态布局是
  // 只看 #list 判出来的，面板打开时它必须整体让位。
  await evalJS(`document.getElementById('agents-btn').click()`);
  await until('空态下后台面板开', async () => (await geo()).cls.includes('agents-view'), 8000);
  g = await geo();
  ck('空态 + 面板打开：聊天面与空态 Hero 让位', !g.shown.list && !g.shown.form && !g.shown.meta, JSON.stringify(g.shown));
  ck('空态 + 面板打开：会话头在主栏内、在文档流', inMain(g) && g.headPos === 'static', `headX=${g.headX} pos=${g.headPos}`);
  await evalJS(`document.getElementById('agents-btn').click()`);
  await until('面板收起（空态）', async () => !(await geo()).cls.includes('browsing'), 8000);

  // ② 有消息：绑定桩项目，绑定应答落进转录区，空态退场
  if ((await send(`/bind ${projDir}`)) !== 202) throw new Error('POST /bind 非 202');
  await until('绑定应答落地', async () => { const x = await geo(); return !x.empty && x.nmsgs >= 1 ? x : null; }, 10000);
  g = await geo();
  ck('有消息时 composer 贴底（底距视口底 ≤40px）', g.form.bottomGap >= 0 && g.form.bottomGap <= 40, `bottomGap=${g.form.bottomGap}`);
  ck('有消息时会话头在主栏内', inMain(g), `headX=${g.headX} sideRight=${g.sideRight} pos=${g.headPos}`);

  // ④ 面板打开：聊天面整体让位，会话头仍在主栏里
  await evalJS(`document.getElementById('agents-btn').click()`);
  await until('后台面板开', async () => (await geo()).cls.includes('agents-view'), 8000);
  g = await geo();
  ck('面板打开：#list 与输入卡片让位（display:none）', !g.shown.list && !g.shown.form, JSON.stringify(g.shown));
  ck('面板打开：composer 计量行让位', !g.shown.meta, JSON.stringify(g.shown));
  if (g.hasHist) ck('面板打开：#hist 让位', !g.shown.hist, JSON.stringify(g.shown));
  ck('面板打开：会话头在主栏内、在文档流', inMain(g) && g.headPos === 'static', `headX=${g.headX} pos=${g.headPos}`);
  await evalJS(`document.getElementById('agents-btn').click()`);
  await until('面板收起', async () => !(await geo()).cls.includes('browsing'), 8000);
  g = await geo();
  ck('面板收起后输入卡片回来', g.shown.form, JSON.stringify(g.shown));
  if (g.hasHist) {
    // ③ 看历史（W6-5 起）：历史渲进 #hist，会话头回文档流、composer 仍贴底可用
    const rowSel = `#sess-list .sess-row[data-sid="${sid}"]`;
    await evalJS(`(() => { const r = document.querySelector(${JSON.stringify(rowSel)}); if (r) r.click(); return !!r; })()`);
    await until('历史视图开', () => evalJS(`document.body.classList.contains('viewing') && document.querySelectorAll('#hist .msg').length >= 1`), 8000);
    g = await geo();
    ck('看历史：会话头在主栏内、在文档流', inMain(g) && g.headPos === 'static', `headX=${g.headX} pos=${g.headPos}`);
    ck('看历史：输入卡片可见且贴底', g.shown.form && g.form.bottomGap >= 0 && g.form.bottomGap <= 40, `bottomGap=${g.form.bottomGap}`);
    await evalJS('viewLive()');
  }
} catch (e) {
  ck('量测流程', false, e.message);
}
console.log(fails.length ? `\n${fails.length} 项量测不过` : '\n全部量测通过（W6-4 composer 与主区几何）');
process.exit(fails.length ? 1 : 0);
