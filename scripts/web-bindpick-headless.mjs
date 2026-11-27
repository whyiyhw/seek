#!/usr/bin/env node
// web-bindpick-headless.mjs — 项目候选点选（c_lnsdvu）的无头浏览器验收半边
//（scripts/web-bindpick-manual.sh 的驱动）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动
// 本机 headless Chrome。交互走真实 UI 路径：按钮/行 .click()、行点选用
// mousedown（页面 pick 挂在 mousedown 上）、输入框赋值后派 input 事件 +
// 点 #send 提交（表单 submit 链）。
//
// 断言景（主 URL：proj-alpha 带 manifest+会话 / proj-beta 仅 config）：
//   基线   → 弹层初始关
//   景 1   → 点 #cmd-bind：弹层开；头行「点选会重置当前会话…」；两行候选
//            （alpha=N 个会话 / beta=未开过）；未绑定 → 无 当前
//   景 2   → Esc 关；手打 /bind alpha + /resume sid → /state 会话 = sid
//   景 3   → 点 #cmd-projects：alpha 行带 当前；点 beta 行（mousedown）→
//            /state project = beta 且 session 清空（/bind 语义 = start over）
//   景 4   → 手打 /bind <不存在路径> → 聊天面「绑定失败：目录不存在：…」
//   景 5   → 键盘：↑↓ 移高亮 + Enter 点选（同一条 pick 路径）
// 空态景（--empty，配第二个无夹具 serve 的 URL）：
//   景 E   → 点 #cmd-bind → 一行「没有可选项目：…」提示
//
// 用法：node web-bindpick-headless.mjs <页面URL> [projA projB sid]
//       [--gone <不存在路径>] [--empty] [--chrome <chrome 可执行文件>]
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
const EMPTY = argv.includes('--empty');
const GONE = flag('--gone', '/tmp/seek-bp-gone');
const pos = argv.filter((a, i) => !a.startsWith('--') && argv[i - 1] !== '--chrome'
  && argv[i - 1] !== '--gone');
const pageUrl = pos[0];
const PROJ_A = pos[1], PROJ_B = pos[2], SID = pos[3];
if (!pageUrl || (!EMPTY && (!PROJ_A || !PROJ_B || !SID))) {
  console.error('usage: web-bindpick-headless.mjs <pageUrl> [projA projB sid] [--gone p] [--empty] [--chrome path]');
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
const dbgPort = EMPTY ? 18786 : 18785;   // 与 web-rename(18796) 等错开；空态景分开跑也不撞
const profile = mkdtempSync(path.join(tmpdir(), 'seek-bp-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', '--window-size=1440,900',
  `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动（${EMPTY ? '空态景' : '全景'}），等 DevTools 目标`);
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
  head: document.querySelector('#menu .mh')?.textContent || '',
  rows: [...document.querySelectorAll('#menu .mi')].map(r => ({
    nm: r.querySelector('.nm')?.textContent || '',
    ds: r.querySelector('.ds')?.textContent || '',
    sid: r.querySelector('.sid')?.textContent || '',
    hl: r.classList.contains('hl'),
  })),
  hlIdx: [...document.querySelectorAll('#menu .mi')].findIndex(r => r.classList.contains('hl')),
  status: document.getElementById('status').textContent,
  chat: document.getElementById('list').innerText,
  inputVal: document.getElementById('input').value,
}))()`);
// /state：页面同源 fetch（cookie 已落），conv 身份从 localStorage 取——
// 与页面自身 refreshState 同一条链。
const state = () => evalJS(`fetch('/state', {headers: {'X-Seek-Conversation':
  localStorage.getItem('seek-web-conv') || ''}}).then(r => r.json())`);
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);
// 弹层行点选：页面 pick 挂 mousedown（防 textarea 先 blur）——合成同款事件。
const mousedownRow = i => evalJS(`(() => {
  const rows = document.querySelectorAll('#menu .mi');
  if (!rows[${i}]) return 'no-row';
  rows[${i}].dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true }));
  return 'ok';
})()`);
const keyOnInput = key => evalJS(`(() => {
  document.getElementById('input').dispatchEvent(
    new KeyboardEvent('keydown', { key: ${JSON.stringify(key)}, bubbles: true, cancelable: true }));
  return 'ok';
})()`);
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

if (EMPTY) {
  // 空态景：两个来源都空 → 一行提示（不静默无弹层）。
  trace('空态景：点 #cmd-bind');
  try {
    await clickSel('#cmd-bind');
    const s = await until('空态提示行', s => !s.menuHidden && s.rows.length === 1, 10000);
    ck('空态：弹层开 + 恰一行提示', s.rows.length === 1
      && s.rows[0].nm.startsWith('没有可选项目：'), JSON.stringify(s.rows));
    ck('空态：头行告知仍在', s.head.includes('点选会重置当前会话'), s.head);
  } catch (e) { ck('空态景', false, e.message); await dump(); }
  console.log(fails.length ? `FAIL：${fails.length} 项未过` : '空态断言全过');
  process.exit(fails.length ? 1 : 0);
}

// 景 0：基线 —— 弹层初始关。
trace('景 0：基线');
try {
  const s = await snapshot();
  ck('基线：弹层初始关', s.menuHidden, '');
} catch (e) { ck('基线', false, e.message); await dump(); }

// 景 1：点 #cmd-bind → 弹层出候选（两行：alpha 会话数 / beta 未开过）。
trace('景 1：#cmd-bind 开弹层');
try {
  await clickSel('#cmd-bind');
  const s = await until('弹层两行候选', s => !s.menuHidden && s.rows.length === 2, 10000);
  ck('弹层开 + 两行候选', s.rows.length === 2, JSON.stringify(s.rows.map(r => r.nm)));
  ck('头行固定告知（会话重置语义）', s.head === '点选会重置当前会话（/bind 语义）；路径也可手打', s.head);
  const alpha = s.rows.find(r => r.nm === 'proj-alpha'), beta = s.rows.find(r => r.nm === 'proj-beta');
  ck('proj-alpha 行：绝对路径 + 1 个会话', !!alpha && alpha.ds.includes(PROJ_A) && alpha.ds.includes('1 个会话'), alpha && alpha.ds);
  ck('proj-beta 行：绝对路径 + 未开过', !!beta && beta.ds.includes(PROJ_B) && beta.ds.includes('未开过'), beta && beta.ds);
  ck('未绑定 → 无 当前', s.rows.every(r => r.sid === ''), JSON.stringify(s.rows.map(r => r.sid)));
  ck('首行高亮（Enter 直选语义）', s.hlIdx === 0, String(s.hlIdx));
} catch (e) { ck('#cmd-bind 弹层', false, e.message); await dump(); }

// 景 2：Esc 关 + 手打绑定 alpha、/resume 夹具会话 → /state 会话 = sid
//（给景 3 的「点行后 session 清空」一个非空的起点）。
trace('景 2：Esc + 手打 /bind + /resume');
try {
  await clickSel('#input');   // openBindPicker 已聚焦输入框，补一次点击无害
  let s = await snapshot();
  if (!s.menuHidden) {
    await keyOnInput('Escape');
    s = await until('Esc 关弹层', s => s.menuHidden, 5000);
    ck('Esc 关闭弹层', s.menuHidden, '');
  }
  await typeAndSend('/bind ' + PROJ_A);
  await until('已绑定 alpha', s => s.chat.includes('已绑定项目'), 8000);
  await typeAndSend('/resume ' + SID);
  await until('已续接会话', s => s.chat.includes('已续接会话'), 8000);
  const st = await state();
  ck('手打 /bind + /resume → /state 会话 = 夹具 sid',
    st.bound === true && st.project === PROJ_A && st.session === SID,
    JSON.stringify({ bound: st.bound, project: st.project, session: st.session }));
} catch (e) { ck('Esc + 手打链', false, e.message); await dump(); }

// 景 3：点 #cmd-projects（第二个触发点）→ alpha 行 当前 → 点 beta 行 →
// /state project = beta 且 session 清空。
trace('景 3：#cmd-projects + 点行换绑');
try {
  await clickSel('#cmd-projects');
  let s = await until('弹层开且 alpha 带 当前', s => !s.menuHidden
    && s.rows.some(r => r.nm === 'proj-alpha' && r.sid === '当前'), 10000);
  ck('#cmd-projects 同样开弹层', s.rows.length === 2, JSON.stringify(s.rows.map(r => r.nm)));
  ck('当前绑定行标 当前（且仅一行）',
    s.rows.filter(r => r.sid === '当前').length === 1
      && s.rows.find(r => r.nm === 'proj-alpha').sid === '当前',
    JSON.stringify(s.rows.map(r => r.nm + '|' + r.sid)));
  const betaIdx = s.rows.findIndex(r => r.nm === 'proj-beta');
  await mousedownRow(betaIdx);
  await until('聊天面出已绑定 beta', s => s.chat.includes('已绑定项目 ' + PROJ_B), 8000);
  const st = await state();
  ck('点行 → /state project = beta', st.project === PROJ_B, String(st.project));
  ck('点行 → 会话 id 清空（/bind = start over）', st.session === '', String(st.session));
  ck('点行 → 弹层已关', (await snapshot()).menuHidden, '');
} catch (e) { ck('点行换绑', false, e.message); await dump(); }

// 景 4：手打不存在路径 → Router 的失败应答如实透传到聊天面（无预检）。
trace('景 4：不存在路径');
try {
  await typeAndSend('/bind ' + GONE);
  const s = await until('绑定失败应答', s => s.chat.includes('绑定失败：目录不存在'), 8000);
  ck('手打不存在路径 → 「绑定失败：目录不存在：…」', s.chat.includes('绑定失败：目录不存在：' + GONE), '');
  const st = await state();
  ck('失败应答后绑定不动（仍是 beta）', st.project === PROJ_B, String(st.project));
} catch (e) { ck('不存在路径', false, e.message); await dump(); }

// 景 5：键盘点选——↑↓ 移高亮、Enter 走同一条 pick（发 /bind）。景 3/4 后
// 绑定停在 beta：键盘挑 alpha 换回去，/state 的 project 变化即独占证据
//（聊天面是累积的，「已绑定 alpha」景 2 已出现过，不能当本次的判据）。
trace('景 5：键盘点选');
try {
  await clickSel('#cmd-bind');
  let s = await until('弹层开', s => !s.menuHidden && s.rows.length === 2, 10000);
  ck('开弹层后输入框聚焦（键盘线）', await evalJS(
    'document.activeElement === document.getElementById("input") ? "1" : ""') === '1', '');
  await keyOnInput('ArrowDown');   // 高亮 0(alpha) → 1(beta)
  s = await until('高亮移到 beta', s => s.hlIdx === 1 && s.rows[1].hl, 5000);
  ck('↓ 移高亮到第二行', s.hlIdx === 1, JSON.stringify(s.rows.map(r => r.hl)));
  await keyOnInput('ArrowDown');   // 再 ↓：回绕到 0(alpha)
  s = await until('高亮回绕到 alpha', s => s.hlIdx === 0, 5000);
  await keyOnInput('Enter');       // Enter = pick(menuHl) → 发 /bind alpha
  await until('键盘点选换回 alpha', async () => (await state()).project === PROJ_A, 8000);
  const st = await state();
  ck('Enter 点选 → 发 /bind（同一条 pick 路径）', st.project === PROJ_A, String(st.project));
  ck('键盘点选 → 弹层已关', (await snapshot()).menuHidden, '');
} catch (e) { ck('键盘点选', false, e.message); await dump(); }

console.log(fails.length ? `FAIL：${fails.length} 项未过` : '全部断言过');
process.exit(fails.length ? 1 : 0);
