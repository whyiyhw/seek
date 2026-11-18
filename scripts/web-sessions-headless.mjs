#!/usr/bin/env node
// web-sessions-headless.mjs — W4.4 资产面 sessions 浏览器的无头浏览器
// 验收半边（scripts/web-sessions-manual.sh 的驱动；feature-web-frontend
// §9 W4.4 验收「手测脚本；长 transcript 分页不卡」）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动
// 本机 headless Chrome。全部交互走真实 UI 路径（按钮 .click()——与真人点
// 击同一条线），脚本不持有 token（URL 由外层脚本传入）。
//
// 断言五景（对应 manual 清单 1–5 + XSS 用例）：
//   进面板   → 📚 会话：browsing 隐聊天、列表两条（新→旧，长会话标题=
//              首条用户消息首行）、指令按钮让位
//   长会话   → 首页 50 条即刻渲染；元信息行 model/turns/tokens；foot
//              「加载更多（已显示 50 条）」
//   翻页走完 → 「加载更多」逐页点到 1234 条全量：每页时延记录（分页不卡
//              的证据——超时 8s 硬失败，>3s 记 FAIL）；页界连续性（第 51
//              条恰在第 2 页首、第 1234 条恰在末页末）= 分页遍历=全文件
//              顺序口径；末页「— 已到末尾 —」
//   切换+XSS → 中途切短会话：2 条即「已到末尾」；<script>/<img onerror>
//              载荷字面渲染、不执行（scripts 不增、无 img、window.__xss
//              undefined）；切回长会话从第 1 页重开、无串台
//   回聊天   → 💬 聊天：聊天面/输入框原样可见（W4.4 加法零回归）
//
// 用法：node scripts/web-sessions-headless.mjs <页面URL> <长会话sid> <短会话sid>
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
const LONG_SID = args[1];
const TINY_SID = args[2];
const N_LONG = 1234;   // 与 web-sessions-manual.sh 夹具同源（50/页 → 24 整页+尾页 34）
if (!pageUrl || !LONG_SID || !TINY_SID) {
  console.error('usage: web-sessions-headless.mjs <pageUrl> <longSid> <tinySid> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const WATCHDOG_MS = 240_000;
// 总闸 watchdog：任何一景卡死都不许把脚本挂成无限等（CDP 无回包、页面
// 死循环等都可能）——超时退出码 1，现场由各景自身的 dump 兜。
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18793;   // W2.1 用 18792；错开免得两脚本并跑互抢
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w44-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动，等 DevTools 目标（${pageUrl}）`);
const chromeErr = [];
chrome.stderr.on('data', d => chromeErr.push(d));
const cleanup = () => { try { chrome.kill(); } catch { /* already gone */ } };
process.on('exit', cleanup);
// 显式接信号再 exit，'exit' 钩子才会跑（默认 SIGTERM 终止不保证跑），
// 免得脚本被外层杀掉时留下孤儿 headless Chrome。
process.on('SIGTERM', () => process.exit(1));
process.on('SIGINT', () => process.exit(1));

let target = null;
for (let i = 0; i < 100 && !target; i++) {
  await delay(200);
  if (chrome.exitCode !== null) break;   // 同步查活：await 死亡 Promise 会把活进程等成死等
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

// 面板快照：全部断言读这一份（与 W2.1 的 snapshot 同构，字段换面板）。
const snapshot = () => evalJS(`(() => ({
  browsing: document.body.classList.contains('browsing'),
  sessView: document.body.classList.contains('sess-view'),
  rows: [...document.querySelectorAll('#sess-list .sess-row')].map(r => ({
    sid: r.dataset.sid, t1: r.querySelector('.t1').textContent,
  })),
  nmsgs: document.getElementById('sess-msgs').childElementCount,
  meta: document.getElementById('sess-meta').textContent,
  foot: document.getElementById('sess-foot').textContent,
  footBtn: !!document.querySelector('#sess-foot button'),
  cmdHidden: getComputedStyle(document.getElementById('cmd-bind')).display === 'none',
  listShown: getComputedStyle(document.getElementById('list')).display !== 'none',
  formShown: getComputedStyle(document.getElementById('form')).display !== 'none',
  browseLabel: document.getElementById('browse-btn').textContent,
  firstMsg: document.querySelector('#sess-msgs .smsg')?.textContent || '',
  lastMsg: [...document.querySelectorAll('#sess-msgs .smsg')].at(-1)?.textContent || '',
  msgAt50: document.querySelectorAll('#sess-msgs .smsg')[50]?.textContent || '',
  nscript: document.scripts.length,
  nimg: document.querySelectorAll('img').length,
  xss1: typeof window.__xss, xss2: typeof window.__xss2,
}))()`);
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);
const clickRow = sid => clickSel(`#sess-list .sess-row[data-sid="${sid}"]`);

// ---- 3. 场景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snapshot(), null, 1)); } catch { /* 页面已关 */ } };

// 就绪：/state 探测落定（不再「连接中」即判定过鉴权；?token= 首跳已被
// 服务端 303 换成 cookie，页面落在干净的 / 上）。
trace('等页面就绪');
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = '';
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
  } catch { /* 页面尚未执行到脚本或导航中，继续轮询 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}
const base = await snapshot();
ck('基线 scripts=1（仅页面内联脚本）', base.nscript === 1, `got ${base.nscript}`);
ck('基线在聊天面（未 browsing）', !base.browsing && base.listShown && base.formShown, '');

// 轮询直到谓词过或超时；返回最后一次快照。
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

// 景 0：进面板 —— browsing 隐聊天 + 列表两条（新→旧）+ 指令按钮让位。
trace('景 0：进面板');
try {
  await clickSel('#browse-btn');
  const s = await until('面板列表', s => s.sessView && s.rows.length === 2, 10000);
  ck('面板开：sess-view + browsing', s.sessView && s.browsing, '');
  ck('聊天面/输入框让位（display:none）', !s.listShown && !s.formShown, '');
  ck('指令按钮让位（W4.4 不与指令并排）', s.cmdHidden, '');
  ck('列表两条（新→旧：长在前）', s.rows[0].sid === LONG_SID && s.rows[1].sid === TINY_SID,
    JSON.stringify(s.rows.map(r => r.sid)));
  ck('长会话标题=首条用户消息首行', s.rows[0].t1.includes('第 1 条：请读一下这段代码并解释。'), s.rows[0].t1);
  ck('短会话标题=首条用户消息', s.rows[1].t1.includes('tiny fixture：你好'), s.rows[1].t1);
} catch (e) { ck('进面板', false, e.message); await dump(); }

// 景 1：长会话首页 —— 50 条即刻渲染 + 元信息行 + foot 计数。
trace('景 1：长会话首页');
try {
  await clickRow(LONG_SID);
  const s = await until('首页 50 条', s => s.nmsgs === 50 && s.meta, 10000);
  ck('首页恰 50 条（不整读 1234）', s.nmsgs === 50, `got ${s.nmsgs}`);
  ck('元信息行 model/turns/tokens/cwd',
    s.meta.includes('deepseek-chat') && s.meta.includes('617 turns')
    && s.meta.includes('270000 tokens') && s.meta.includes('/tmp/w44-fixture'), s.meta);
  ck('foot「加载更多（已显示 50 条）」', s.foot.includes('已显示 50 条'), s.foot);
  ck('首条是第 1 条用户消息', s.firstMsg.includes('user') && s.firstMsg.includes('第 1 条'), s.firstMsg);
} catch (e) { ck('长会话首页', false, e.message); await dump(); }

// 景 2：翻页走完 —— 逐页点到 1234，页界连续 + 每页时延。
trace('景 2：翻页走完（24 页「加载更多」）');
const PAGE_TIMEOUT = 8000, PAGE_SLOW = 3000;
try {
  const times = [];
  for (;;) {
    const s = await snapshot();
    if (!s.footBtn) break;                     // 「— 已到末尾 —」
    if (s.foot.includes('失败')) throw new Error('foot 报错：' + s.foot);
    const before = s.nmsgs;
    const t0 = Date.now();
    await clickSel('#sess-foot button');
    await until('下一页', s => s.nmsgs > before, PAGE_TIMEOUT);
    times.push(Date.now() - t0);
  }
  const s = await snapshot();
  ck('全量 1234 条（无重无漏）', s.nmsgs === N_LONG, `got ${s.nmsgs}`);
  ck('末页「— 已到末尾 —」', s.foot.includes('已到末尾'), s.foot);
  ck('页界连续：第 51 条恰在第 2 页首（cursor=50 不偏）', s.msgAt50.includes('第 51 条'), s.msgAt50);
  ck('末条恰是第 1234 条（全文件顺序口径）', s.lastMsg.includes('第 1234 条'), s.lastMsg);
  const max = Math.max(...times), med = times.slice().sort((a, b) => a - b)[times.length >> 1];
  ck(`分页不卡：${times.length} 页全走完，每页 <${PAGE_SLOW}ms`, max < PAGE_SLOW,
    `max=${max}ms med=${med}ms（超时阈值 ${PAGE_TIMEOUT}ms 未触发）`);
  console.log(`  ·   分页时延：max=${max}ms 中位=${med}ms（${times.length} 页）`);
} catch (e) { ck('翻页走完', false, e.message); await dump(); }

// 景 3：切换 + XSS —— 短会话 2 条即末尾；注入载荷字面渲染不执行；切回
// 长会话从第 1 页重开（分页游标随会话重置，无串台）。
trace('景 3：切换 + XSS');
try {
  await clickRow(TINY_SID);
  const s = await until('短会话 2 条', s => s.nmsgs === 2 && s.foot.includes('已到末尾'), 8000);
  ck('短会话 2 条即「已到末尾」', s.nmsgs === 2, `got ${s.nmsgs} foot=${s.foot}`);
  ck('XSS：<script> 字面渲染为文本', s.lastMsg.includes('<script>window.__xss=1</script>'), s.lastMsg);
  ck('XSS：<img onerror> 字面渲染为文本', s.lastMsg.includes('<img src=x onerror='), '');
  ck('XSS：脚本未执行（window.__xss undefined）', s.xss1 === 'undefined', `got ${s.xss1}`);
  ck('XSS：onerror 未执行（window.__xss2 undefined）', s.xss2 === 'undefined', `got ${s.xss2}`);
  ck('XSS：document.scripts 不增（仍 1）', s.nscript === 1, `got ${s.nscript}`);
  ck('XSS：无 img 元素落地', s.nimg === 0, `got ${s.nimg}`);
  await clickRow(LONG_SID);
  const back = await until('切回长会话首页', s => s.nmsgs === 50, 8000);
  ck('切回长会话从第 1 页重开（无串台）', back.nmsgs === 50 && back.firstMsg.includes('第 1 条'),
    `nmsgs=${back.nmsgs}`);
} catch (e) { ck('切换 + XSS', false, e.message); await dump(); }

// 景 4：回聊天 —— 面板收起，聊天面/输入框原样（W4.4 是加法，零回归）。
trace('景 4：回聊天');
try {
  await clickSel('#browse-btn');   // 活动态下按钮已是「💬 聊天」
  const s = await until('回聊天面', s => !s.browsing, 5000);
  ck('面板收起（browsing/sess-view 清零）', !s.browsing && !s.sessView, '');
  ck('聊天面/输入框原样可见', s.listShown && s.formShown, '');
  ck('按钮复归「📚 会话」', s.browseLabel.includes('会话'), s.browseLabel);
} catch (e) { ck('回聊天', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（W4.4 无头验收）');
process.exit(fails.length ? 1 : 0);
