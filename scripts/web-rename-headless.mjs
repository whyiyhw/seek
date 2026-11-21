#!/usr/bin/env node
// web-rename-headless.mjs — 会话重命名/归档的无头浏览器验收半边
// （scripts/web-rename-manual.sh 的驱动；W5 卡 c_rqcuaa 验收②：
// UI 操作往返——改名→列表刷新、归档→隐藏→切查看可见、取消归档、
// Esc 取消、归档可见位刷新保持）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议
// 驱动本机 headless Chrome。交互走真实 UI 路径（元素 .click() /
// input/keydown 事件）；显示态操作钮（hover 才显）用 .click() 直发——
// 程序化 click 不受 display:none 限制，与真用户 hover 后点按同 handler。
//
// 断言景：
//   基线 → 两条夹具行；每行 .sess-acts 内 ✏/🗄 两钮；头部 🗄 关
//   改名 → ✏ 进编辑态（预填旧值）；改字回车 → 列表刷新出「浏览器改名」
//   取消 → 再进编辑态改字按 Esc → 标题不变
//   归档 → 🗄 → 行从列表消失；头部 🗄 亮 → 行回来（.arch 变淡 +
//          「已归档 ·」前缀 + ⤴ 钮）且沉底
//   复归 → ⤴ → 行回正常态（无前缀、无 .arch）
//   记忆 → 归档可见位开着时整页刷新 → 仍开（localStorage）
//
// 用法：node scripts/web-rename-headless.mjs <页面URL> <改名sid> <归档sid>
//       [--chrome <chrome 可执行文件>]（默认 CHROME env 或 macOS 标准位）
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const chromeIdx = argv.indexOf('--chrome');
const CHROME = chromeIdx > 0 ? argv[chromeIdx + 1]
  : process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const args = argv.filter((a, i) => a !== '--chrome' && i !== chromeIdx + 1);
const pageUrl = args[0];
const REN_SID = args[1];
const ARC_SID = args[2];
if (!pageUrl || !REN_SID || !ARC_SID) {
  console.error('usage: web-rename-headless.mjs <pageUrl> <renSid> <arcSid> [--chrome path]');
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
const dbgPort = 18796;   // 与 web-sessions(18793)/ctxmeter 等错开
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5ren-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', '--window-size=1440,900',
  `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动，等 DevTools 目标（${pageUrl}）`);
const chromeErr = [];
chrome.stderr.on('data', d => chromeErr.push(d));
const cleanup = () => { try { chrome.kill(); } catch { /* already gone */ } };
process.on('exit', cleanup);
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

// 面板快照：全部断言读这一份。
const snapshot = () => evalJS(`(() => {
  const row = sid => document.querySelector('#sess-list .sess-row[data-sid="' + sid + '"]');
  const ren = row(${JSON.stringify(REN_SID)});
  const arc = row(${JSON.stringify(ARC_SID)});
  return {
    rows: [...document.querySelectorAll('#sess-list .sess-row')].map(r => ({
      sid: r.dataset.sid, t1: r.querySelector('.t1')?.textContent || '',
      arch: r.classList.contains('arch'),
      acts: r.querySelectorAll('.sess-acts button').length,
    })),
    renTitle: ren ? ren.querySelector('.t1').textContent : null,
    renActs: ren ? [...ren.querySelectorAll('.sess-acts button')].map(b => b.title) : [],
    arcGone: !arc,
    arcArch: arc ? arc.classList.contains('arch') : null,
    arcT2: arc ? (arc.querySelector('.t2')?.textContent || '') : null,
    arcUnarchBtn: arc ? !!arc.querySelector('.sess-acts button[title="取消归档"]') : false,
    editOpen: !!document.querySelector('.sess-edit'),
    editVal: document.querySelector('.sess-edit')?.value ?? null,
    archOn: document.getElementById('side-arch').classList.contains('on'),
    archTitle: document.getElementById('side-arch').title,
    status: document.getElementById('status').textContent,
  };
})()`);
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);
const typeEdit = (val, key) => evalJS(`(() => {
  const el = document.querySelector('.sess-edit');
  if (!el) return 'no-input';
  el.value = ${JSON.stringify(val)};
  el.dispatchEvent(new KeyboardEvent('keydown', { key: ${JSON.stringify(key)}, bubbles: true }));
  return 'ok';
})()`);
const reload = () => evalJS('location.reload(), "ok"');

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

const actSel = (sid, titlePrefix) =>
  `#sess-list .sess-row[data-sid="${sid}"] .sess-acts button[title^="${titlePrefix}"]`;

// 景 0：基线 —— 两行齐、行内两钮、头部 🗄 关。
trace('景 0：基线');
try {
  const s = await until('侧栏两行', s => s.rows.length === 2, 10000);
  ck('两条夹具行都在', s.rows.every(r => r.sid === REN_SID || r.sid === ARC_SID), JSON.stringify(s.rows.map(r => r.sid)));
  ck('每行行内 ✏/🗄 两钮', s.rows.every(r => r.acts === 2), JSON.stringify(s.rows.map(r => r.acts)));
  ck('头部 🗄 缺省关', !s.archOn && s.archTitle.includes('显示'), s.archTitle);
  ck('改名行标题=派生（首条用户消息）', s.renTitle.includes('原始标题'), s.renTitle);
} catch (e) { ck('基线', false, e.message); await dump(); }

// 景 1：行内改名 —— ✏ → 预填旧值 → 改字回车 → 列表刷新出新标题。
trace('景 1：改名提交');
try {
  await clickSel(actSel(REN_SID, '重命名'));
  let s = await until('编辑态', s => s.editOpen, 5000);
  ck('✏ 切进编辑态且预填旧标题', s.editVal !== null && s.editVal.includes('原始标题'), JSON.stringify(s.editVal));
  await typeEdit('浏览器改名✅', 'Enter');
  s = await until('列表刷新出新标题', s => (s.renTitle || '').includes('浏览器改名✅'), 8000);
  ck('回车提交 → 整表刷新出新标题', s.renTitle.includes('浏览器改名✅'), s.renTitle);
  ck('编辑态退出', !s.editOpen, '');
} catch (e) { ck('改名提交', false, e.message); await dump(); }

// 景 2：Esc 取消 —— 改字按 Esc → 标题不变。
trace('景 2：Esc 取消');
try {
  await clickSel(actSel(REN_SID, '重命名'));
  await until('编辑态', s => s.editOpen, 5000);
  await typeEdit('这条不该落库', 'Escape');
  const s = await until('编辑态退出', s => !s.editOpen, 5000);
  ck('Esc 取消 → 标题不变', s.renTitle.includes('浏览器改名✅'), s.renTitle);
} catch (e) { ck('Esc 取消', false, e.message); await dump(); }

// 景 3：归档 —— 🗄 → 行消失；头部 🗄 → 行回来（变淡+前缀+⤴）沉底。
trace('景 3：归档往返');
try {
  await clickSel(actSel(ARC_SID, '归档（'));
  let s = await until('归档行从默认列表消失', s => s.arcGone && s.rows.length === 1, 8000);
  ck('🗄 → 行隐藏（默认列表只剩 1 行）', s.arcGone && s.rows.length === 1, JSON.stringify(s.rows.map(r => r.sid)));
  await clickSel('#side-arch');
  s = await until('切查看后归档行回来', s => !s.arcGone, 8000);
  ck('头部 🗄 亮 + 行回来', s.archOn && !s.arcGone, '');
  ck('归档行变淡 + 「已归档 ·」前缀 + ⤴ 钮', s.arcArch && s.arcT2.includes('已归档 ·') && s.arcUnarchBtn,
    `arch=${s.arcArch} t2=${s.arcT2}`);
  ck('归档行沉底（活跃行在上）', s.rows.at(-1).sid === ARC_SID, JSON.stringify(s.rows.map(r => r.sid)));
} catch (e) { ck('归档往返', false, e.message); await dump(); }

// 景 4：归档可见位刷新保持 + 取消归档恢复。
trace('景 4：记忆 + 取消归档');
try {
  await reload();
  let s = await until('刷新后列表回来', s => s.rows.length === 2, 10000);
  ck('刷新后归档可见位保持（localStorage）', s.archOn, '');
  ck('刷新后归档行仍可见且带标', !s.arcGone && s.arcArch && s.arcUnarchBtn, '');
  await clickSel(actSel(ARC_SID, '取消归档'));
  s = await until('取消归档恢复常态', s => !s.arcGone && !s.arcArch, 8000);
  ck('⤴ → 行回常态（无 .arch、无 ⤴ 钮）', !s.arcArch && !s.arcUnarchBtn, `arch=${s.arcArch}`);
  ck('头部 🗄 仍开（可见位独立于行状态）', s.archOn, '');
} catch (e) { ck('记忆+取消归档', false, e.message); await dump(); }

console.log(fails.length ? `FAIL：${fails.length} 项未过` : '全部断言过');
process.exit(fails.length ? 1 : 0);
