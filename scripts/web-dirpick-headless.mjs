#!/usr/bin/env node
// web-dirpick-headless.mjs — /bind 目录浏览器的无头浏览器验收半边
// （scripts/web-dirpick-manual.sh 的驱动；卡 c_heffj3 验收③）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议
// 驱动本机 headless Chrome。交互走真实 UI 路径：草稿用 value+input
// 事件（页面监听 input）、菜单行用 mousedown（renderMenu 的行监听
// mousedown，el.click() 只发 click 不行）、层头按钮用 .click()。
//
// 断言景：
//   入口 → 侧栏头「工作区」钮（#side-ws-new）直接进目录模式；树只列有痕迹的
//          工作区，第一个工作区必然没痕迹，所以这枚钮是树的补集
//   层头 → 路径=首个可浏览根（project_roots[0]）；↑ 在根上禁用
//   过滤 → 过滤框打字收窄行；清空恢复
//   下钻 → proja → sub1 两级；路径/行随层更新
//   上一级 → 回 proja
//   绑定 → 「选这个目录」→ /state 的 project 变该路径且 session 清空
//   失败 → 点一个已被外部删除的目录行 → 层内一行错误提示，不白屏
//   Esc → 关层
//
// 用法：node scripts/web-dirpick-headless.mjs <页面URL> <夹具根(物理路径)>
//       [--chrome <chrome 可执行文件>]（默认 CHROME env 或 macOS 标准位）
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { rmSync } from 'node:fs';
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
const ROOT = args[1];   // 夹具根（物理路径）：默认浏览起点 = project_roots[0] = <ROOT>/dirpick
if (!pageUrl || !ROOT) {
  console.error('usage: web-dirpick-headless.mjs <pageUrl> <fixtureRoot> [--chrome path]');
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
const dbgPort = 18802;   // 与 web-rename(18796)/ctxmeter 等错开
const profile = mkdtempSync(path.join(tmpdir(), 'seek-dirpick-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', '--window-size=1440,900',
  `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动，等 DevTools 目标（${pageUrl}）`);
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

// 弹层快照：全部断言读这一份。
const snapshot = () => evalJS(`(() => {
  const m = document.getElementById('menu');
  const head = m.querySelector('.dirhead');
  return {
    menuOpen: !m.hidden,
    rows: [...m.querySelectorAll('.mi .nm')].map(x => x.textContent),
    head: head ? {
      path: head.querySelector('.dirpath').textContent,
      upDisabled: head.querySelectorAll('.dirtop button')[0].disabled,
      pickDisabled: head.querySelectorAll('.dirtop button')[1].disabled,
    } : null,
    filterVal: head ? head.querySelector('.dirfilter').value : null,
    status: document.getElementById('status').textContent,
    draft: document.getElementById('input').value,
  };
})()`);
const state = () => evalJS(`fetch('/state', { headers: { 'X-Seek-Conversation': CONV } })
  .then(r => r.json()).then(s => ({ project: s.project || '', session: s.session || '', bound: !!s.bound }))`);
// 草稿驱动：value + input 事件（页面监听 input→updateMenu）
const setDraft = t => evalJS(`(() => {
  const i = document.getElementById('input');
  i.value = ${JSON.stringify(t)};
  i.dispatchEvent(new Event('input', { bubbles: true }));
  return 'ok';
})()`);
// 菜单行点击：行监听 mousedown（click 前会 blur 关层，el.click() 不行）
const clickRow = label => evalJS(`(() => {
  const row = [...document.querySelectorAll('#menu .mi')]
    .find(r => (r.querySelector('.nm') || {}).textContent === ${JSON.stringify(label)});
  if (!row) return 'no-row';
  row.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
  return 'ok';
})()`);
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);
const clickHeadBtn = i => evalJS(`(() => {
  const b = document.querySelectorAll('#menu .dirhead .dirtop button')[${i}];
  if (!b) return 'no-btn';
  b.click();
  return 'ok';
})()`);
const setFilter = v => evalJS(`(() => {
  const f = document.querySelector('#menu .dirfilter');
  if (!f) return 'no-filter';
  f.value = ${JSON.stringify(v)};
  f.dispatchEvent(new Event('input', { bubbles: true }));
  return 'ok';
})()`);
const pressKey = (key, sel) => evalJS(`(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return 'no-el';
  el.dispatchEvent(new KeyboardEvent('keydown', { key: ${JSON.stringify(key)}, bubbles: true }));
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
  while (!pred(s)) {
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(100);
    s = await snapshot();
  }
  return s;
}

// 就绪：状态行不再是「连接中」（?token= 首跳已被 303 换 cookie）。
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = '';
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
  } catch { /* 页面未就绪，继续轮询 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}

// 景 0：入口 —— 侧栏头的「工作区」钮直接开目录模式（工作区树卡：入口
// 从「手打 /bind 的参数期」搬到了这枚钮，树只列有痕迹的工作区，第一个
// 工作区必然没痕迹，所以这枚钮是树的补集）。
trace('景 0：入口（#side-ws-new）');
try {
  await clickSel('#side-ws-new');
  let s = await until('目录层打开', s => s.head !== null, 8000);
  ck('#side-ws-new 直接开目录模式（无「浏览目录…」中转行）', s.head !== null, JSON.stringify(s));
  let st = await state();
  ck('基线未绑定', !st.bound && st.project === '', JSON.stringify(st));
} catch (e) { ck('入口', false, e.message); await dump(); }

// 景 1：进目录模式 —— 层头路径=首个可浏览根；根上 ↑ 禁用；行=子目录。
trace('景 1：目录模式层头');
try {
  let s = await until('首层读取落定', s => s.head && s.head.path.endsWith('/dirpick')
    && !s.rows.includes('（读取中…）') && s.rows.length === 2, 8000);
  ck('层头路径=首个可浏览根', s.head.path.endsWith('/dirpick'), s.head.path);
  ck('根上「↑ 上一级」禁用', s.head.upDisabled, JSON.stringify(s.head));
  ck('行=子目录（proja/probj，文件与点条目被滤）',
    s.rows.includes('proja') && s.rows.includes('probj') && s.rows.length === 2,
    JSON.stringify(s.rows));
} catch (e) { ck('目录模式层头', false, e.message); await dump(); }

// 景 2：过滤 —— 打字收窄、清空恢复。
trace('景 2：过滤');
try {
  await setFilter('proja');
  let s = await snapshot();
  ck('过滤收窄到一行', s.rows.length === 1 && s.rows[0] === 'proja', JSON.stringify(s.rows));
  await setFilter('');
  s = await snapshot();
  ck('清空过滤恢复两行', s.rows.length === 2, JSON.stringify(s.rows));
} catch (e) { ck('过滤', false, e.message); await dump(); }

// 景 3：下钻两级（proja → sub1）。
trace('景 3：下钻');
try {
  await clickRow('proja');
  let s = await until('进 proja', s => s.head && s.head.path.endsWith('/proja'), 8000);
  await until('proja 行落定', s => s.rows.includes('sub1') && s.rows.includes('vanish'), 8000);
  ck('proja 行=sub1/sub2/vanish', s.rows.length === 3, JSON.stringify(s.rows));
  await clickRow('sub1');
  s = await until('进 sub1', s => s.head && s.head.path.endsWith('/sub1'), 8000);
  ck('下钻两级到 sub1', s.head.path.endsWith('/proja/sub1'), s.head.path);
  ck('深层「↑ 上一级」可用', !s.head.upDisabled, JSON.stringify(s.head));
} catch (e) { ck('下钻', false, e.message); await dump(); }

// 景 4：上一级 —— sub1 回 proja；↑ 在键位上也可用（层内导航复用）。
trace('景 4：上一级');
try {
  await clickHeadBtn(0);
  let s = await until('回 proja', s => s.head && s.head.path.endsWith('/proja'), 8000);
  ck('「↑ 上一级」回 proja', s.head.path.endsWith('/dirpick/proja'), s.head.path);
} catch (e) { ck('上一级', false, e.message); await dump(); }

// 景 5：选这个目录 —— /bind 透传 → /state 的 project 变该路径、session 清空。
trace('景 5：绑定');
try {
  await clickHeadBtn(1);
  const want = ROOT + '/dirpick/proja';
  let st = null;
  for (let i = 0; i < 80; i++) {
    st = await state();
    if (st.bound && st.project === want) break;
    await delay(150);
  }
  ck('/state.project = 选中的目录', st && st.project === want, JSON.stringify(st));
  ck('/state.session 清空（/bind 重置会话）', st && st.session === '', JSON.stringify(st));
  const s = await snapshot();
  ck('绑定后弹层已关', !s.menuOpen, JSON.stringify(s));
  ck('草稿里的半截 /bind 已清', s.draft === '', JSON.stringify(s.draft));
} catch (e) { ck('绑定', false, e.message); await dump(); }

// 景 6：失败分支 —— 先列后删 vanish，点行 → 层内一行错误，不白屏。
trace('景 6：目录消失');
try {
  await clickSel('#side-ws-new');
  await until('首层读取落定', s => s.head && s.head.path.endsWith('/dirpick')
    && !s.rows.includes('（读取中…）') && s.rows.length === 2, 8000);
  await clickRow('proja');
  await until('proja 行落定', s => s.rows.includes('vanish'), 8000);
  rmSync(ROOT + '/dirpick/proja/vanish', { recursive: true, force: true });
  await clickRow('vanish');
  const s = await until('层内错误提示', s => s.rows.length === 1
    && s.rows[0].startsWith('（') && !s.rows[0].includes('读取中'), 8000);
  ck('目录消失 → 层内一行错误', s.rows[0].includes('不是目录或目录不存在'), s.rows[0]);
  ck('弹层仍开（不外抛不白屏）', s.menuOpen && !!s.head, JSON.stringify(s));
} catch (e) { ck('目录消失', false, e.message); await dump(); }

// 景 7：Esc 关层（层内导航键就地消化）。
trace('景 7：Esc');
try {
  await pressKey('Escape', '#menu .dirfilter');
  const s = await snapshot();
  ck('Esc 关目录层', !s.menuOpen, JSON.stringify(s));
} catch (e) { ck('Esc', false, e.message); await dump(); }

console.log(fails.length ? `FAIL：${fails.length} 项未过` : '全部断言过');
process.exit(fails.length ? 1 : 0);
