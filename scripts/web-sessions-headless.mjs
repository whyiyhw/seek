#!/usr/bin/env node
// web-sessions-headless.mjs — 会话侧栏的无头浏览器验收半边
// （scripts/web-sessions-manual.sh 的驱动；W5 卡 c_txz6vz 验收①–③：
// 侧栏全套交互 / 收起状态刷新后保持 / 原 📚 弹出代码退役）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动
// 本机 headless Chrome。全部交互走真实 UI 路径（元素 .click() / input 事件
// ——与真人操作同一条线），脚本不持有 token（URL 由外层脚本传入）。
//
// 断言景（对应 manual 清单 1–7 + XSS 用例）：
//   基线     → 侧栏常驻展开：列表按 updated 倒序、首屏 30 行 + 「加载更多」，
//              长会话标题=首条用户消息；聊天面原样可见；📚 按钮已退役
//   会话正文 → 点长会话：中栏切正文（聊天面让位），首页 50 条 + 元信息行 +
//              「加载更多（已显示 50 条）」；该行在侧栏高亮
//   翻页走完 → 逐页点到 1234 条：页界连续、末页「— 已到末尾 —」、每页时延
//   切换+XSS → 短会话 2 条即末尾；注入载荷字面渲染不执行；切回长会话重开
//   搜索     → 输入 tiny：列表只剩短会话；清空恢复
//   收起记忆 → 点 ⟨：侧栏成 44px 图标轨；刷新后仍收起；展开后刷新仍展开
//   新建     → 点 ＋新建：发出 /new 同款指令（未绑定时得未绑定提示）
//   回聊天   → 💬 聊天：聊天面/输入框原样可见（侧栏是加法，零回归）
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
const N_ALL = 32;      // 夹具总数：长 + 短 + 30 条 old（侧栏 30/页 → 有「加载更多」）
const SESS_PAGE = 30;
if (!pageUrl || !LONG_SID || !TINY_SID) {
  console.error('usage: web-sessions-headless.mjs <pageUrl> <longSid> <tinySid> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
// BASE_SCRIPTS：页面基线脚本数。W5 c_ni7mzw 的 head 主题 bootstrap 是第三个
// 内联块（防 FOUC 必须在样式表前同步跑），与 markdown.js、页面脚本并列。
// XSS 断言拿它当基线：渲染管线不许再增一个 script。
const BASE_SCRIPTS = 3;
const WATCHDOG_MS = 240_000;
// 总闸 watchdog：任何一景卡死都不许把脚本挂成无限等（CDP 无回包、页面
// 死循环等都可能）——超时退出码 1，现场由各景自身的 dump 兜。
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
// 视口固定 1440×900：侧栏缺省展开的判定是 <1024px 收起，给出宽视口才测
// 得到「首次访问默认展开」这一半（窄屏缺省由 CSS/JS 同断点保证）。
const dbgPort = 18793;   // W2.1 用 18792；错开免得两脚本并跑互抢
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5side-chrome.'));
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
// 通用 CDP 调用（Emulation.setDeviceMetricsOverride 改视口用——窄屏缺省
// 收起是卡片验收面，必须真改视口读，不能只信源码字符串）。
async function cdp(method, params) {
  return new Promise((res, rej) => {
    const id = ++seq;
    pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method, params: params || {} }));
  });
}

// 面板快照：全部断言读这一份。侧栏字段（collapsed/rows/foot）+ 中栏字段
// （detail 开否、消息数、meta/foot）+ 聊天面可见性 + 退役面（no 📚）。
const snapshot = () => evalJS(`(() => {
  const side = document.getElementById('side');
  const list = document.getElementById('sess-list');
  const vis = el => el ? getComputedStyle(el).display !== 'none' : false;
  return {
    collapsed: document.body.classList.contains('side-collapsed'),
    sideW: Math.round(side.getBoundingClientRect().width),
    listShown: vis(list),
    rows: [...document.querySelectorAll('#sess-list .sess-row')].map(r => ({
      sid: r.dataset.sid, t1: r.querySelector('.t1').textContent,
      t2: (r.querySelector('.t2l') || {}).textContent || '',
      cur: r.classList.contains('cur'),
    })),
    sideFoot: document.getElementById('side-foot').textContent,
    sideFootBtn: !!document.querySelector('#side-foot button'),
    sideHint: document.getElementById('sess-list').textContent,
    // 工作区树：组头（名字/会话数/当前态）
    groups: [...document.querySelectorAll('#sess-list .ws-head')].map(h => ({
      root: h.dataset.root,
      name: (h.querySelector('.ws-name') || {}).textContent || '',
      cnt: (h.querySelector('.ws-cnt') || {}).textContent || '',
      cur: h.classList.contains('cur'),
      addBtn: !!h.querySelector('.ws-acts button'),
    })),
    searchVal: document.getElementById('side-search').value,
    detail: document.body.classList.contains('sess-detail'),
    browserShown: vis(document.getElementById('browser')),
    chatShown: vis(document.getElementById('list')),
    formShown: vis(document.getElementById('form')),
    nmsgs: document.getElementById('sess-msgs').childElementCount,
    meta: document.getElementById('sess-meta').textContent,
    foot: document.getElementById('sess-foot').textContent,
    footBtn: !!document.querySelector('#sess-foot button'),
    chatText: document.getElementById('list').textContent,
    firstMsg: document.querySelector('#sess-msgs .smsg')?.textContent || '',
    lastMsg: [...document.querySelectorAll('#sess-msgs .smsg')].at(-1)?.textContent || '',
    msgAt50: document.querySelectorAll('#sess-msgs .smsg')[50]?.textContent || '',
    nscript: document.scripts.length,
    nimg: document.querySelectorAll('img').length,
    xss1: typeof window.__xss, xss2: typeof window.__xss2,
    browseBtn: !!document.getElementById('browse-btn'),
  };
})()`);
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);
const clickRow = sid => clickSel(`#sess-list .sess-row[data-sid="${sid}"]`);
const typeSearch = v => evalJS(`(() => {
  const el = document.getElementById('side-search');
  el.value = ${JSON.stringify(v)};
  el.dispatchEvent(new Event('input'));
  return 'ok';
})()`);

// ---- 3. 场景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snapshot(), null, 1)); } catch { /* 页面已关 */ } };

// 就绪：/state 探测落定（不再「连接中」即判定过鉴权；?token= 首跳已被
// 服务端 303 换成 cookie，页面落在干净的 / 上）。
async function waitReady(what) {
  trace('等页面就绪（' + what + '）');
  for (let i = 0; i < 100; i++) {
    await delay(150);
    let ready = '';
    try {
      ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
    } catch { /* 页面尚未执行到脚本或导航中，继续轮询 */ }
    if (ready) return;
  }
  console.error('FAIL: 15s 页面未就绪（' + what + '）');
  process.exit(1);
}
await waitReady('首次加载');

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

// 景 0：基线 —— 侧栏常驻展开 + 列表 30 行（updated 倒序）+ 聊天面原样。
trace('景 0：基线侧栏');
try {
  const s = await until('侧栏列表', s => s.rows.length >= SESS_PAGE, 10000);
  ck('侧栏常驻且缺省展开（宽视口）', !s.collapsed && s.sideW > 200, `w=${s.sideW}`);
  ck('侧栏列表首屏 30 行 + 「加载更多」',
    s.rows.length === SESS_PAGE && s.sideFootBtn && s.sideFoot.includes('已显示 30 条'),
    `rows=${s.rows.length} foot=${s.sideFoot}`);
  ck('列表按 updated 倒序：长会话在前、短会话次之',
    s.rows[0].sid === LONG_SID && s.rows[1].sid === TINY_SID,
    JSON.stringify(s.rows.slice(0, 3).map(r => r.sid)));
  ck('长会话标题=首条用户消息首行', s.rows[0].t1.includes('第 1 条：请读一下这段代码并解释。'), s.rows[0].t1);
  ck('短会话标题=首条用户消息', s.rows[1].t1.includes('tiny fixture：你好'), s.rows[1].t1);
  // c_jf3grt 文案口径：侧栏行 = 短日期（MM-DD HH:mm，无秒、不吃 locale）
  // + 「N 轮」（中文无量词单复数，不再有 "1 turns"）。
  ck('侧栏行：日期短格式 + 「N 轮」（无 "turns"）',
    /\d{2}-\d{2} \d{2}:\d{2}/.test(s.rows[0].t2) && s.rows[0].t2.includes('617 轮')
    && !s.rows[0].t2.includes('turns') && s.rows[1].t2.includes('1 轮'),
    JSON.stringify([s.rows[0].t2, s.rows[1].t2]));
  ck('聊天面/输入框原样可见（零回归）', s.chatShown && s.formShown && !s.detail, '');
  ck('原 📚 弹出按钮已退役（DOM 无 browse-btn）', !s.browseBtn, '');
  // 加载更多：30 → 32 行（夹具总数），按钮随后消失。
  await clickSel('#side-foot button');
  const all = await until('侧栏全量 32 行', s => s.rows.length === N_ALL, 8000);
  ck('「加载更多」把侧栏补到全量 32 行', all.rows.length === N_ALL, `got ${all.rows.length}`);
  ck('补满后翻页按钮消失', !all.sideFootBtn, all.sideFoot);
} catch (e) { ck('基线侧栏', false, e.message); await dump(); }

// 景 1：长会话首页 —— 中栏切正文 + 50 条 + 元信息行 + 侧栏高亮。
trace('景 1：长会话首页');
try {
  await clickRow(LONG_SID);
  const s = await until('首页 50 条', s => s.detail && s.nmsgs === 50 && s.meta, 10000);
  ck('点行切中栏正文（聊天面/输入框让位，侧栏不动）',
    s.detail && s.browserShown && !s.chatShown && !s.formShown && s.listShown, '');
  ck('首页恰 50 条（不整读 1234）', s.nmsgs === 50, `got ${s.nmsgs}`);
  ck('元信息行 model/轮次/tokens/cwd',
    s.meta.includes('deepseek-chat') && s.meta.includes('617 轮')
    && s.meta.includes('270000 tokens') && s.meta.includes('/tmp/w44-fixture'), s.meta);
  ck('foot「加载更多（已显示 50 条）」', s.foot.includes('已显示 50 条'), s.foot);
  ck('首条是第 1 条用户消息', s.firstMsg.includes('user') && s.firstMsg.includes('第 1 条'), s.firstMsg);
  ck('侧栏当前项高亮（恰一条，且是长会话）',
    s.rows.filter(r => r.cur).length === 1 && s.rows.find(r => r.cur)?.sid === LONG_SID,
    JSON.stringify(s.rows.filter(r => r.cur).map(r => r.sid)));
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
  ck('XSS：document.scripts 不增（仍 ' + BASE_SCRIPTS + '）', s.nscript === BASE_SCRIPTS, `got ${s.nscript}`);
  ck('XSS：无 img 元素落地', s.nimg === 0, `got ${s.nimg}`);
  ck('短会话行接棒高亮', s.rows.find(r => r.cur)?.sid === TINY_SID,
    JSON.stringify(s.rows.filter(r => r.cur).map(r => r.sid)));
  await clickRow(LONG_SID);
  const back = await until('切回长会话首页', s => s.nmsgs === 50, 8000);
  ck('切回长会话从第 1 页重开（无串台）', back.nmsgs === 50 && back.firstMsg.includes('第 1 条'),
    `nmsgs=${back.nmsgs}`);
} catch (e) { ck('切换 + XSS', false, e.message); await dump(); }

// 景 4：标题搜索 —— 输入即过滤；清空恢复全量（并从第一页重开）。
trace('景 4：标题搜索');
try {
  await typeSearch('tiny');
  const s = await until('过滤到 1 行', s => s.rows.length === 1, 5000);
  ck('搜索「tiny」只剩短会话', s.rows[0].sid === TINY_SID, JSON.stringify(s.rows.map(r => r.sid)));
  await typeSearch('OLD FIXTURE #7');
  const c = await until('大小写无关子串命中', s => s.rows.length === 1, 5000);
  ck('搜索大小写无关、子串命中（OLD FIXTURE #7）',
    c.rows[0].t1.includes('old fixture #7'), c.rows[0].t1);
  await typeSearch('');
  const all = await until('清空恢复', s => s.rows.length === SESS_PAGE, 5000);
  ck('清空搜索恢复首屏 30 行', all.rows.length === SESS_PAGE, `got ${all.rows.length}`);
  await typeSearch('zzz-no-such-title');
  const none = await until('无匹配', s => s.rows.length === 0, 5000);
  ck('无匹配显示提示而非空列表', none.sideHint.includes('没有匹配的'), none.sideHint);
  await typeSearch('');
} catch (e) { ck('标题搜索', false, e.message); await dump(); }

// 景 5：收起 / 展开 + 刷新记忆（验收②）。
trace('景 5：收起展开 + 刷新保持');
try {
  await clickSel('#side-toggle');
  const s = await until('收起', s => s.collapsed && s.sideW <= 48, 5000);
  ck('收起：body.side-collapsed + 44px 图标轨 + 列表让位',
    s.collapsed && s.sideW <= 48 && !s.listShown, `w=${s.sideW} listShown=${s.listShown}`);
  const stored = await evalJS(`localStorage.getItem('seek-side-collapsed')`);
  ck('收起位已写入 localStorage', stored === '1', `got ${stored}`);
  // 刷新页面：同一 profile、同一 URL —— localStorage 必须把收起态带回来。
  await evalJS('location.reload(), "ok"');
  await delay(400);
  await waitReady('刷新后');
  const r = await until('刷新后仍收起', s => s.collapsed && s.rows.length >= SESS_PAGE, 10000);
  ck('刷新后收起状态保持（验收②）', r.collapsed && r.sideW <= 48, `collapsed=${r.collapsed} w=${r.sideW}`);
  ck('收起态下侧栏仍按 30 行挂载（展开即用）', r.rows.length === SESS_PAGE, `rows=${r.rows.length}`);
  await clickSel('#side-toggle');
  // 宽度是 150ms 过渡：等类翻面 + 宽度落定再断言（读同一份快照即可）。
  const e = await until('展开', s => !s.collapsed && s.sideW > 200, 5000);
  ck('展开回 260px 且列表可见', !e.collapsed && e.sideW > 200 && e.listShown && e.rows.length >= SESS_PAGE,
    `w=${e.sideW} listShown=${e.listShown} rows=${e.rows.length}`);
  await evalJS('location.reload(), "ok"');
  await delay(400);
  await waitReady('二次刷新后');
  const e2 = await until('刷新后仍展开', s => !s.collapsed && s.rows.length >= SESS_PAGE, 10000);
  ck('刷新后展开状态同样保持（两个方向都记）', !e2.collapsed && e2.sideW > 200, `w=${e2.sideW}`);
  const stored2 = await evalJS(`localStorage.getItem('seek-side-collapsed')`);
  ck('展开位已写回 localStorage', stored2 === '0', `got ${stored2}`);
} catch (e) { ck('收起展开 + 刷新保持', false, e.message); await dump(); }

// 景 6：组头 = 「去这个工作区」（工作区树卡）。夹具里 32 条会话同属一个
// 工作区（cwd 都是 /tmp/w44-fixture），未绑定状态下点组头 → 该组有会话，
// 所以发的是 /resume <最新一条>（绑定跟着会话自记的 cwd 走，上下文不丢），
// 而不是 /bind（/bind 会重置会话——点一下组头就清掉正在进行的对话是错的）。
trace('景 6：点组头 = 去这个工作区');
try {
  const base = await until('树上有组头', s => s.groups.length >= 1, 10000);
  ck('侧栏是两级树：恰一个工作区组头，会话数 32',
    base.groups.length === 1 && base.groups[0].cnt === String(N_ALL),
    JSON.stringify(base.groups));
  ck('组头名 = cwd 的 basename', base.groups[0].name === 'w44-fixture', base.groups[0].name);
  ck('组头带 ＋（在该工作区开新会话）', base.groups[0].addBtn, JSON.stringify(base.groups));
  await clickSel('#sess-list .ws-head');
  const s = await until('续接回执', s => s.chatText.includes('已续接会话'), 10000);
  ck('点组头 → 发 /resume（不是 /bind：不重置上下文）',
    s.chatText.includes('已续接会话 ' + LONG_SID), s.chatText.slice(-160));
  const st = await evalJS(`fetch('/state', {headers: {'X-Seek-Conversation':
    localStorage.getItem('seek-web-conv') || ''}}).then(r => r.json())`);
  ck('绑定会话翻成组内最新一条', st.session === LONG_SID, JSON.stringify(st.session));
  ck('绑定项目跟到会话自记的 cwd', st.project === '/tmp/w44-fixture', String(st.project));
  const after = await until('组头标当前', s => s.groups.some(g => g.cur), 8000);
  ck('当前工作区的组头高亮（左缘 accent 条）', after.groups.find(g => g.cur)?.name === 'w44-fixture',
    JSON.stringify(after.groups.map(g => g.name + '|' + g.cur)));
} catch (e) { ck('组头去工作区', false, e.message); await dump(); }

// 景 7：回聊天 —— 会话正文收起，聊天面/输入框原样（侧栏是加法，零回归）。
trace('景 7：回聊天');
try {
  // 先开一条会话正文，再点 💬 聊天 收回。
  await clickRow(LONG_SID);
  await until('正文打开', s => s.detail && s.nmsgs === 50, 8000);
  await clickSel('#sess-back');
  const s = await until('回聊天面', s => !s.detail, 5000);
  ck('💬 聊天：会话正文收起、聊天面/输入框回来',
    !s.detail && !s.browserShown && s.chatShown && s.formShown, '');
  ck('回聊天后侧栏仍在（常驻面）', s.listShown && s.rows.length >= SESS_PAGE, `rows=${s.rows.length}`);
  ck('刚才那条会话仍高亮（当前项随会话走）', s.rows.find(r => r.cur)?.sid === LONG_SID,
    JSON.stringify(s.rows.filter(r => r.cur).map(r => r.sid)));
} catch (e) { ck('回聊天', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（W5 c_txz6vz 会话侧栏无头验收）');
if (fails.length) process.exit(1);

// 景 8：窄屏缺省收起 —— 清掉记忆位，把视口压到 900px，刷新：侧栏应自己
// 收起（验收「窄屏默认收起」）。之后恢复宽视口再刷新，回到展开。
trace('景 8：窄屏缺省收起');
try {
  await evalJS(`localStorage.removeItem('seek-side-collapsed'), 'ok'`);
  await cdp('Emulation.setDeviceMetricsOverride',
    { width: 900, height: 720, deviceScaleFactor: 1, mobile: false });
  await evalJS('location.reload(), "ok"');
  await delay(500);
  await waitReady('窄屏刷新后');
  const n = await until('窄屏缺省收起', s => s.collapsed && s.sideW <= 48, 10000);
  ck('窄屏 900px、无记忆位 → 缺省收起（44px 轨）', n.collapsed && n.sideW <= 48, `w=${n.sideW}`);
  const stored = await evalJS(`localStorage.getItem('seek-side-collapsed')`);
  ck('缺省收起不写记忆位（用户没做过选择）', stored === null, `got ${stored}`);
  // 收窄视口下聊天面仍在（收起只是让位，不是隐藏界面）。
  ck('窄屏收起时聊天面/输入框可用', n.chatShown && n.formShown, '');
  // 宽视口复位：清覆盖后刷新回到展开（记忆位仍为空 → 宽度缺省）。
  await cdp('Emulation.clearDeviceMetricsOverride');
  await evalJS('location.reload(), "ok"');
  await delay(500);
  await waitReady('宽屏复位后');
  const w = await until('宽屏缺省展开', s => !s.collapsed && s.sideW > 200, 10000);
  ck('宽视口复位 → 缺省展开（同一个宽度断点两向都验）', !w.collapsed && w.sideW > 200, `w=${w.sideW}`);
} catch (e) {
  ck('窄屏缺省收起', false, e.message);
  await dump();
  process.exit(1);
}

process.exit(0);
