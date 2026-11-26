#!/usr/bin/env node
// web-trend-headless.mjs — 跨会话趋势图（W5 卡 c_6xktw4）的无头浏览器
// 验收半边（scripts/web-trend-manual.sh 的驱动）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动
// 本机 headless Chrome；交互一律走真实 UI 路径（元素 .click() 与 CDP 的
// Input.dispatchMouseEvent——与真人操作同一条线）。脚本不持有 token
// （URL 由外层脚本传入）。
//
// 三景（对应 manual 清单，每景一份 SEEK_HOME 夹具，由外层脚本起停 serve）：
//   multi  → 4 个会话：趋势卡手绘 SVG 出三条折线 + 8 个点圆；
//            悬停出 guide 线 + tooltip（sid/时间/tok/$/缓存）；窗口切到
//            近 10 / 近 50 后重绘且点数不变（夹具只有 4 个）；
//            单会话快照卡片仍在（趋势是加法，不是替换）；侧栏每行出 cost 列
//   single → 1 个会话：一个点、无折线、「折线需要 ≥2 个点」文案
//   empty  → 空 store：趋势卡空态文案 + 快照区「没有历史会话」文案，不报错
//
// 用法：node scripts/web-trend-headless.mjs <页面URL> <multi|single|empty>
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
const MODE = args[1];
if (!pageUrl || !['multi', 'single', 'empty'].includes(MODE)) {
  console.error('usage: web-trend-headless.mjs <pageUrl> <multi|single|empty> [--chrome path]');
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
const dbgPort = 18798;   // 18792–18797 已被既有 W 脚本占用
const profile = mkdtempSync(path.join(tmpdir(), 'seek-trend-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', '--window-size=1200,900',
  `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动，等 DevTools 目标`);
const chromeErr = [];
chrome.stderr.on('data', d => chromeErr.push(d));
const cleanup = () => { try { chrome.kill(); } catch { /* already gone */ } };
process.on('exit', cleanup);
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

// ---- 2. CDP：最小 Runtime.evaluate + Input 客户端 ----
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
async function cdp(method, params) {
  return new Promise((res, rej) => {
    const id = ++seq;
    pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method, params: params || {} }));
  });
}
async function evalJS(expr) {
  const r = await cdp('Runtime.evaluate',
    { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) {
    throw new Error('page eval failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  }
  return r.result.value;
}
const clickSel = sel => evalJS(
  `(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);

// 面板快照：全部断言读这一份。plot 内省 SVG 形状（polyline/圆点/文案），
// tip 内省 tooltip；侧栏读每行的 cost 单元格。
const snapshot = () => evalJS(`(() => {
  const plot = document.getElementById('trend-plot');
  const svg = plot && plot.querySelector('svg');
  const tip = document.getElementById('trend-tip');
  const cards = document.getElementById('stat-cards');
  return {
    plotText: plot ? plot.textContent : '',
    hasSvg: !!svg,
    n: svg ? Number(svg.dataset.n) : -1,
    cur: svg ? svg.dataset.cur : '',
    polylines: svg ? svg.querySelectorAll('polyline').length : 0,
    dots: svg ? svg.querySelectorAll('circle').length : 0,
    guideVisible: svg ? [...svg.querySelectorAll('.tguide')].some(g => g.getAttribute('visibility') === 'visible') : false,
    tipHidden: tip ? tip.hidden : true,
    tipText: tip ? tip.textContent : '',
    winBtns: [...document.querySelectorAll('#trend-tools .tbtn')].map(b => ({ w: b.dataset.win, on: b.classList.contains('on') })),
    cardText: cards ? cards.textContent : '',
    costCells: [...document.querySelectorAll('#sess-list .sess-row')].map(r => (r.title.match(/\\$[\\d.]+/) || [''])[0]),
    nRows: document.querySelectorAll('#sess-list .sess-row').length,
    statsShown: getComputedStyle(document.getElementById('stats')).display !== 'none',
  };
})()`);

// ---- 3. 景驱动 + 断言 ----
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
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）：${JSON.stringify(s)}`);
    await delay(120);
    s = await snapshot();
  }
  return s;
}

trace('等页面就绪（/state 探测落定）');
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = '';
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
  } catch { /* 页面尚未执行到脚本 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}

// 打开统计面板：入口按钮走真实点击。
trace('打开统计面板');
await clickSel('#stat-btn');

if (MODE === 'multi') {
  try {
    const s = await until('趋势图渲染', x => x.hasSvg && x.n === 4, 10000);
    ck('统计面板点亮', s.statsShown, '');
    ck('趋势 SVG 手绘：4 个会话点', s.n === 4, `n=${s.n}`);
    ck('三条折线（tokens / $ / 缓存命中）', s.polylines === 3, `polylines=${s.polylines}`);
    ck('数据点圆点 3×4=12', s.dots === 12, `dots=${s.dots}`);
    ck('窗口切换三档、缺省「近 20」点亮',
      s.winBtns.length === 3 && s.winBtns.find(b => b.on)?.w === '20', JSON.stringify(s.winBtns));
    ck('空态/单点文案不出现（4 个点走折线path）',
      !s.plotText.includes('还没有历史会话') && !s.plotText.includes('折线需要'), s.plotText);

    // 悬停：CDP 真鼠标移到最后一个点的 x（按 SVG viewBox 折算成屏幕像素
    // ——比按百分比猜更稳，图的左右留白和 x 轴端点不影响）。
    const box = await evalJS(`(() => {
      const svg = document.querySelector('#trend-plot svg');
      const r = svg.getBoundingClientRect();
      const W = svg.viewBox.baseVal.width;
      const dots = [...svg.querySelectorAll('circle.tdot')];
      const cx = Number(dots[dots.length - 1].getAttribute('cx'));
      return { x: r.left + (cx / W) * r.width, y: r.top + r.height / 2, left: r.left };
    })()`);
    const hx = Math.round(box.x), hy = Math.round(box.y);
    await cdp('Input.dispatchMouseEvent', { type: 'mouseMoved', x: hx, y: hy, buttons: 0 });
    const hov = await until('悬停 tooltip', x => !x.tipHidden, 5000);
    ck('悬停：tooltip 现身', !hov.tipHidden, '');
    ck('悬停：guide 线可见', hov.guideVisible, '');
    ck('悬停：tooltip 带 sid/时间/tok/$/缓存',
      /^[0-9a-z]{8} · /.test(hov.tipText) && hov.tipText.includes('tok ')
      && hov.tipText.includes('$') && hov.tipText.includes('缓存 ')
      && hov.tipText.includes('%'), hov.tipText);
    ck('悬停：命中索引=最后一点（3）', hov.cur === '3', `cur=${hov.cur}`);
    // 移出：tooltip 收起、guide 隐藏。
    await cdp('Input.dispatchMouseEvent', { type: 'mouseMoved', x: Math.round(box.left - 40), y: hy, buttons: 0 });
    const off = await until('悬停收起', x => x.tipHidden, 5000);
    ck('移出：tooltip 收起、guide 隐藏', off.tipHidden && !off.guideVisible, JSON.stringify(off));

    // 窗口切换：点「近 50」→ 重新拉取（夹具 4 个，点数不变）、按钮态跟随。
    await clickSel('#trend-tools .tbtn[data-win="50"]');
    const w50 = await until('窗口 50 生效', x => x.winBtns.find(b => b.on)?.w === '50', 8000);
    ck('窗口切换：近 50 点亮、图仍 4 个点', w50.n === 4, `n=${w50.n}`);
    await clickSel('#trend-tools .tbtn[data-win="10"]');
    const w10 = await until('窗口 10 生效', x => x.winBtns.find(b => b.on)?.w === '10', 8000);
    ck('窗口切换：近 10 点亮、图仍 4 个点', w10.n === 4, `n=${w10.n}`);

    // 单会话快照是加法：趋势卡下面三张卡片仍在。
    const cards = await until('快照卡片仍在', x => x.cardText.includes('前缀缓存'), 8000);
    ck('趋势是加法：单会话快照卡片仍在（前缀缓存/tokens/成本）',
      cards.cardText.includes('前缀缓存') && cards.cardText.includes('累计 tokens')
      && cards.cardText.includes('累计花费'), cards.cardText.slice(0, 120));

    // 侧栏 per-session cost 列。
    const side = await until('侧栏 cost 列', x => x.costCells.length === x.nRows && x.nRows >= 4, 10000);
    ck('侧栏每行有 cost 列且都是 $ 数',
      side.costCells.every(t => /^\$[0-9]/.test(t)), JSON.stringify(side.costCells.slice(0, 4)));
    ck('侧栏 cost 列有非零值（有 usage 的会话）',
      side.costCells.some(t => t !== '$0'), JSON.stringify(side.costCells.slice(0, 4)));
  } catch (e) { ck('multi 景', false, e.message); await dump(); }
}

if (MODE === 'single') {
  try {
    // until 条件与下面的断言同口径：趋势点先到、快照卡片后到（两张卡的
    // 数据源是两次 /rpc，页面并发拉），只等 SVG 就断言 cardText 是
    // 「跑两次挂一次」的竞态——断言口径缺陷，不是实现缺陷。
    const s = await until('单点渲染 + 快照卡就绪',
      x => x.hasSvg && x.n === 1 && x.cardText.includes('前缀缓存'), 10000);
    ck('单会话：SVG 一个点、无折线', s.n === 1 && s.polylines === 0, `n=${s.n} lines=${s.polylines}`);
    ck('单会话：文案「折线需要 ≥2 个点」', s.plotText.includes('折线需要 ≥2 个点'), s.plotText);
    ck('单会话：不是空态', !s.plotText.includes('还没有历史会话'), s.plotText);
    ck('单会话：快照卡片仍渲染', s.cardText.includes('前缀缓存'), s.cardText.slice(0, 80));
  } catch (e) { ck('single 景', false, e.message); await dump(); }
}

if (MODE === 'empty') {
  try {
    // 同 single 景：until 口径覆盖下面每条断言用到的快照字段，否则
    // 「空态文案到了、快照区还在加载中」就是一次假红。
    const s = await until('空态文案 + 快照区空文案',
      x => x.plotText.includes('还没有历史会话') && x.cardText.includes('没有历史会话'), 10000);
    ck('空 store：趋势卡空态文案', s.plotText.includes('还没有历史会话'), s.plotText);
    ck('空 store：不渲染 SVG（没有 point 可画）', !s.hasSvg, `hasSvg=${s.hasSvg}`);
    ck('空 store：快照区同口径空文案（不报错）',
      s.cardText.includes('没有历史会话') && !s.cardText.includes('失败'), s.cardText.slice(0, 120));
  } catch (e) { ck('empty 景', false, e.message); await dump(); }
}

console.log(fails.length ? `\n${fails.length} 项断言失败（${MODE}）` : `\n全部断言通过（c_6xktw4 ${MODE}）`);
process.exit(fails.length ? 1 : 0);
