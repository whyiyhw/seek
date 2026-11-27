#!/usr/bin/env node
// web-ctxmeter-headless.mjs — W5 卡 c_oyp2qf 顶栏 ContextMeter 的无头
// 浏览器验收半边（scripts/web-ctxmeter-manual.sh 的驱动）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱
// 动本机 headless Chrome；消息一律用页面自己的 fetch(q('/msg')) 发出。
// 杀 SSE 演练由 --kill/--start 两条 shell 命令承载：kill 真停 serve（页
// 面侧断线），start 拉起第二个 serve（同 SEEK_HOME 同口同 token），验
// 「灯黄→提示条→手动重连→自动恢复转绿→续流」全链。
//
// 断言五景：
//   余量条   → usage 事件驱动：模型·车道/百分比/余量/条宽即时刷新
//   两车道   → 同家族两 wire id 的车道名各自正确（直连/网关）
//   色阶     → 65% 黄档、85% 红档、11% 回绿档（budget 0.6/0.75 口径）
//   断线     → kill serve：灯黄 + 提示条 + 手动重连钮；点它仍黄（服务
//              端停着）不崩
//   恢复续流 → start serve：灯自动转绿、条消失；再发消息应答照常到达
//
// 用法：node web-ctxmeter-headless.mjs <页面URL> <桩项目路径>
//       --kill <cmd> --start <cmd> [--chrome <path>]
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { disposeHeadlessChrome } from './web-headless-chrome.mjs';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const opt = name => {
  const i = argv.indexOf(name);
  const v = i > 0 ? argv[i + 1] : undefined;
  // 从位置参数里剔除选项及其值
  if (i > 0) { argv.splice(i, 2); }
  return v;
};
const CHROME = opt('--chrome') || process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const KILL_CMD = opt('--kill');
const START_CMD = opt('--start');
const pageUrl = argv[0];
const projDir = argv[1];
if (!pageUrl || !projDir || !KILL_CMD || !START_CMD) {
  console.error('usage: web-ctxmeter-headless.mjs <pageUrl> <projDir> --kill <cmd> --start <cmd> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const sh = cmd => spawn('sh', ['-c', cmd], { stdio: ['ignore', 'ignore', 'inherit'] });

const WATCHDOG_MS = 150_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18797;   // 18792–18794 既有脚本占用
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5c-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动，等 DevTools 目标`);
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
const send = text => evalJS(
  `fetch(q('/msg'), { method: 'POST',
     headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
     body: JSON.stringify({ text: ${JSON.stringify(text)} }) }).then(r => r.status)`);

// 顶栏三件 + 连接态的页面快照。
const snapshot = () => evalJS(`(() => {
  const ctx = document.getElementById('ctx');
  const fill = ctx.querySelector('.bar i');
  const conn = document.getElementById('conn');
  return {
    connCls: conn.className,
    bannerHidden: document.getElementById('connbar').hidden,
    ctxHidden: ctx.hidden,
    ctxCls: ctx.className,
    m: document.getElementById('ctx-m').textContent,
    p: document.getElementById('ctx-p').textContent,
    w: fill.style.width,
    usage: document.getElementById('usage').textContent,
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
    await delay(150);
    s = await snapshot();
  }
  return s;
}

trace('等页面就绪（SSE 判定过鉴权并已挂流）');
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = false;
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? false : true)`);
  } catch { /* 页面尚未执行到脚本，继续轮询 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}

// 景 0：绿灯初亮——首次 onopen 后灯绿、提示条藏（首连前不弹条）。
try {
  const s = await until('首连绿灯', s => s.connCls === 'ok', 8000);
  ck('连接灯：连上转绿', s.connCls === 'ok', JSON.stringify(s));
  ck('提示条：已连时隐藏', s.bannerHidden === true, '');
} catch (e) { ck('首连绿灯', false, e.message); await dump(); }

// 景 1：/bind + 车道直连——余量条首次现身：模型·车道名、65% 黄档、余量 350K。
trace('景 1：/bind + 车道直连');
try {
  if ((await send(`/bind ${projDir}`)) !== 202) throw new Error('/bind 非 202');
  await delay(400);   // 等 bind 应答落定（不产 usage，余量条不动）
  if ((await send('车道直连')) !== 202) throw new Error('POST 非 202');
  const s = await until('余量条现身', s => !s.ctxHidden, 10000);
  ck('余量条：模型·车道显示（deepseek-flash · DeepSeek 直连）',
    s.m === 'deepseek-flash · DeepSeek 直连', JSON.stringify(s.m));
  ck('余量条：65% 黄档（budget ≥0.6 口径）',
    s.ctxCls === 'warn' && s.p.startsWith('65%'), `cls=${s.ctxCls} p=${s.p}`);
  ck('余量条：余量读数 350K', s.p.includes('余 350K'), s.p);
  ck('余量条：条宽 65%', s.w === '65%', `w=${s.w}`);   // CSSOM 把 65.0% 规范化成 65%
} catch (e) { ck('车道直连场景', false, e.message); await dump(); }

// 景 2：车道网关——同家族另一 wire id，车道名切换、条不重置。
trace('景 2：车道网关');
try {
  if ((await send('车道网关')) !== 202) throw new Error('POST 非 202');
  const s = await until('车道切换', s => s.m.includes('OpenCode'), 10000);
  ck('两车道样例：deepseek-v4.1-flash · OpenCode 网关',
    s.m === 'deepseek-v4.1-flash · OpenCode 网关', JSON.stringify(s.m));
  ck('两车道样例：占用仍 65%（末轮口径，不累计）',
    s.ctxCls === 'warn' && s.p.startsWith('65%'), `cls=${s.ctxCls} p=${s.p}`);
} catch (e) { ck('车道网关场景', false, e.message); await dump(); }

// 景 3：色阶——吃紧 85% 红档、宽裕 11% 回绿档。
trace('景 3：色阶');
try {
  if ((await send('吃紧')) !== 202) throw new Error('POST 非 202');
  const hot = await until('85% 红档', s => s.ctxCls === 'crit', 10000);
  ck('色阶：85% 红档（budget ≥0.75 口径）',
    hot.ctxCls === 'crit' && hot.p.startsWith('85%'), `cls=${hot.ctxCls} p=${hot.p}`);
  if ((await send('宽裕')) !== 202) throw new Error('POST 非 202');
  const cool = await until('11% 绿档', s => s.ctxCls === '', 10000);
  ck('色阶：11% 回绿档（无 warn/crit 类）',
    cool.ctxCls === '' && cool.p.startsWith('11%'), `cls=${cool.ctxCls} p=${cool.p}`);
} catch (e) { ck('色阶场景', false, e.message); await dump(); }

// 景 4：杀 SSE 演练——真停 serve：灯黄 + 提示条；手动重连（服务端停着）
// 立即重试一次仍黄、页面不崩。
trace('景 4：杀 serve');
try {
  sh(KILL_CMD);
  const s = await until('断线转黄', s => s.connCls === 'warn' && s.bannerHidden === false, 10000);
  ck('断线：灯转黄', s.connCls === 'warn', JSON.stringify(s));
  ck('断线：提示条出现（SSE 已断开 + 手动重连）',
    s.bannerHidden === false
      && await evalJS(`document.getElementById('connbar').textContent.includes('SSE 已断开')`)
      && await evalJS(`!!document.getElementById('reconn')`), '');
  await evalJS(`document.getElementById('reconn').click()`);
  await delay(1200);   // 新连接立刻 ECONNREFUSED → onerror → 仍黄
  const s2 = await snapshot();
  ck('手动重连：服务端停着时保持黄、不崩', s2.connCls === 'warn', JSON.stringify(s2));
} catch (e) { ck('断线场景', false, e.message); await dump(); }

// 景 5：恢复续流——拉起第二个 serve（同口同 token）：EventSource 自动重试
// （retry 3s）转绿、条收起；再发「宽裕」应答照常到达、余量条刷新——
// 断线前后同一个页面会话的流无缝续上。
trace('景 5：重启 serve 续流');
try {
  sh(START_CMD);
  const up0 = Date.now();
  for (;;) {   // node 侧先等 serve 活过来（页面重试才有目标）
    try {
      await fetch(pageUrl, { signal: AbortSignal.timeout(1000), redirect: 'follow' });
      break;
    } catch { /* 未起，继续 */ }
    if (Date.now() - up0 > 15000) throw new Error('15s 内 serve 未活回来');
    await delay(300);
  }
  const s = await until('自动恢复转绿', s => s.connCls === 'ok' && s.bannerHidden === true, 15000);
  ck('恢复：灯自动转绿（EventSource 自带重试）', s.connCls === 'ok', JSON.stringify(s));
  ck('恢复：提示条收起', s.bannerHidden === true, '');
  if ((await send('宽裕')) !== 202) throw new Error('POST 非 202');
  const s2 = await until('续流：余量条再刷新', s => s.p.startsWith('11%'), 10000);
  ck('续流：新应答到达、余量条随 usage 再刷新', s2.p.startsWith('11%') && s2.ctxCls === '', s2.p);
  ck('续流：usage 计量仍在累计（断线前后同一页面会话）',
    await evalJS(`document.getElementById('usage').textContent.includes('tok')`), '');
} catch (e) { ck('恢复续流场景', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（W5 c_oyp2qf 无头验收）');
process.exit(fails.length ? 1 : 0);
