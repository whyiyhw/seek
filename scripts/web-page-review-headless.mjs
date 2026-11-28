#!/usr/bin/env node
// web-page-review-headless.mjs — seek web 页面无头视觉审查的浏览器半边
// （scripts/web-page-review.sh 的驱动，卡 c_vcnacz）。不做断言式验收——
// 它的产出是给人/定时挑刺 agent 看的证据包：
//
//   截图矩阵  亮/暗 × 1440 桌面/390 手机宽 × 8 面板（聊天空态/侧栏/
//             富会话/记忆/统计/后台/设置/审计）= 32 张 PNG，逐镜头落盘；
//   console   全程收集（console.* / 未捕获异常 / 网络资源错误），按
//             「当时在拍哪个镜头」打标 → console.txt；
//   DOM dump  富会话面（亮色桌面、工具树行展开后）整页 outerHTML →
//             dom-chat-rich.html。
//
// 退出码 0 = 32 镜头全部拍齐（面板渲染异常是审查发现，记录在 manifest
// 的 shots[].error 里，不折损退出码——坏页面正是这个工具要抓的东西）；
// 1 = 驱动级失败（Chrome 起不来 / 页面不就绪 / /bind 失败 / 镜头缺失）。
//
// 零依赖：Node ≥22（全局 WebSocket）+ 本机 headless Chrome（CDP），
// 与 web-md-headless.mjs 家族同一套最小 CDP 客户端。
//
// 用法：node scripts/web-page-review-headless.mjs <页面URL> <桩项目路径> \
//            <证据目录> [--rich-sid <富会话id>] [--chrome <路径>] \
//            [--dbg-port <CDP端口>]
// Env:  PAGE_REVIEW_GIT_REV   写进 manifest 的 git 短 rev（缺省 unknown）
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync, writeFileSync } from 'node:fs';
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
const DBG_PORT = parseInt(opt('--dbg-port', '18850'), 10);
const RICH_SID = opt('--rich-sid', '');
const posArgs = argv.filter((a, i) => !a.startsWith('--')
  && argv[i - 1] !== '--chrome' && argv[i - 1] !== '--dbg-port' && argv[i - 1] !== '--rich-sid');
const pageUrl = posArgs[0], projDir = posArgs[1], outDir = posArgs[2];
if (!pageUrl || !projDir || !outDir) {
  console.error('usage: web-page-review-headless.mjs <pageUrl> <projDir> <outDir> '
    + '[--rich-sid id] [--chrome path] [--dbg-port N]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket)');
  process.exit(1);
}

// ---- 视觉趋势计数的阈值常量（manifest.visual）----
// 与 butler 仓 scripts/page-review.mjs 同值，两侧各一份不共享库（跨仓共享
// 模块成本 > 收益）。五项计数只记录不判失败：它们是「页面长什么样」的逐轮
// 旁证，退出码仍只看 32/32 镜头是否拍齐。
const VISUAL_THRESHOLDS = { minContrast: 4.5 };   // WCAG 2.2 AA 正文对比度

// 面板矩阵：8 面板 × 2 主题 × 2 视口 = 32 镜头。聊天空态必须拍在 /bind
// 之前（绑定应答泡会污染空态），所以它是 Phase A 独立拍；其余 7 面板在
// Phase C（/bind 后）按 主题→视口→面板 巡拍。
const THEMES = ['light', 'dark'];
const VIEWS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'mobile', width: 390, height: 844 },
];
// 面板导航表：pane=webfrontend panes 表里的键（button id 前缀 + body 类 +
// load 函数名）。ready=该面板「数据已落 DOM」的谓词。sidebar/chat-rich
// 不走 pane 闸，各自的 ready 见 navigate() 内分支。
const PANELS = [
  { key: 'sidebar', btn: '', cls: '', loadFn: '', label: '侧栏' },
  { key: 'chat-rich', btn: '', cls: '', loadFn: '', label: '富会话' },
  { key: 'memory', btn: 'mem-btn', cls: 'mem-view', loadFn: 'loadMemPane', label: '记忆' },
  { key: 'stats', btn: 'stat-btn', cls: 'stat-view', loadFn: 'loadStatPane', label: '统计' },
  { key: 'agents', btn: 'agents-btn', cls: 'agents-view', loadFn: 'loadAgentsPane', label: '后台' },
  { key: 'settings', btn: 'set-btn', cls: 'settings-view', loadFn: 'loadSetPane', label: '设置' },
  { key: 'hooks', btn: 'hk-btn', cls: 'hooks-view', loadFn: 'loadHooksPane', label: '审计' },
];
const WATCHDOG_MS = 300_000;
const shotMeta = [];          // manifest.shots 的真源
const consoleLog = [];        // {t, phase, kind, text}
let phase = 'startup';

const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（phase=${phase}）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const profile = mkdtempSync(path.join(tmpdir(), 'seek-review-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${DBG_PORT}`,
  `--user-data-dir=${profile}`, pageUrl,
], { stdio: ['ignore', 'ignore', 'pipe'] });
const chromeErr = [];
chrome.stderr.on('data', d => chromeErr.push(d));
// 收尾（杀 Chrome + 删一次性 profile）走全族共用的 disposeHeadlessChrome——
// 为什么不是一句 rmSync 见该模块头注释。
process.on('exit', () => disposeHeadlessChrome(chrome, profile));
process.on('SIGTERM', () => process.exit(1));
process.on('SIGINT', () => process.exit(1));

let target = null;
for (let i = 0; i < 100 && !target; i++) {
  await delay(200);
  if (chrome.exitCode !== null) break;
  try {
    const list = await (await fetch(`http://127.0.0.1:${DBG_PORT}/json/list`,
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

// ---- 2. CDP：最小客户端（请求-响应 + 事件订阅）----
const ws = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0;
const pending = new Map();
const listeners = [];                       // (method, params) => void
ws.onmessage = e => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) {
    const { res, rej } = pending.get(m.id);
    pending.delete(m.id);
    m.error ? rej(new Error(m.error.message)) : res(m.result);
    return;
  }
  if (m.method) for (const fn of listeners) fn(m.method, m.params);
};
async function call(method, params = {}) {
  return new Promise((res, rej) => {
    const id = ++seq;
    pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method, params }));
  });
}
async function evalJS(expr) {
  const r = await call('Runtime.evaluate',
    { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.exceptionDetails) {
    throw new Error('page eval failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  }
  return r.result.value;
}
const $id = id => `document.getElementById('${id}')`;

// console/异常/资源错误收集（审查证据，不是驱动失败）：
// Runtime.consoleAPICalled 覆盖 console.*；Log.entryAdded 覆盖网络层
// 资源错误（favicon 404 这类）；Runtime.exceptionThrown 覆盖未捕获异常。
const argText = args => (args || []).map(a => {
  if (a.value !== undefined) return typeof a.value === 'object'
    ? JSON.stringify(a.value) : String(a.value);
  return a.description || a.type || '?';
}).join(' ');
listeners.push((method, params) => {
  if (method === 'Runtime.consoleAPICalled') {
    consoleLog.push({ t: new Date().toISOString(), phase,
      kind: 'console.' + params.type, text: argText(params.args) });
  } else if (method === 'Runtime.exceptionThrown') {
    const d = params.exceptionDetails;
    consoleLog.push({ t: new Date().toISOString(), phase, kind: 'exception',
      text: (d.exception && (d.exception.description || d.exception.value)) || d.text || '?' });
  } else if (method === 'Log.entryAdded') {
    const en = params.entry;
    consoleLog.push({ t: new Date().toISOString(), phase,
      kind: 'net.' + en.level, text: en.text + (en.url ? ' ' + en.url : '') });
  }
});
await call('Runtime.enable');
await call('Log.enable');
await call('Page.enable');

// ---- 3. 基础动作 ----
async function waitReady(timeoutMs = 15_000) {
  for (let i = 0; i < 100; i++) {
    await delay(150);
    let ok = false;
    try {
      ok = await evalJS(`!${$id('status')}.textContent.includes('连接中')`);
    } catch { /* 导航中，继续轮询 */ }
    if (ok) return;
  }
  throw new Error(`页面 ${timeoutMs}ms 未就绪`);
}
async function setTheme(theme) {
  // 主题真源是 localStorage['seek-theme']（head 内联 bootstrap 在样式表
  // 解析前读它落 data-theme）——设值后整页重载，首帧即目标主题，不走
  // 设置面板的切换钮（那会多一次闪变采样点）。先等就绪再碰
  // localStorage：CDP 目标挂上时页面可能还停在 about:blank，那时读
  // localStorage 是 SecurityError（KEEP 测试首撞）。
  await waitReady();
  await evalJS(`localStorage.setItem('seek-theme', ${JSON.stringify(theme)})`);
  await evalJS('location.reload()');
  await waitReady();
}
async function setViewport(view) {
  await call('Emulation.setDeviceMetricsOverride', {
    width: view.width, height: view.height, deviceScaleFactor: 1, mobile: false,
  });
  // 侧栏开合态：页面加载时按 innerWidth<1024 缺省（W6-8：<1024 自动收轨、
  // 不写偏好），而 setTheme 的 reload 发生在视口覆盖生效之前——不显式钉住，
  // 桌面镜头会顶着手机宽时算出的「已收起」态（首跑实测踩中）。按目标宽度
  // 设回自然态：桌面展开、手机收起（真人首访在各自宽度下看到的样子）。
  await evalJS(`setSide(${view.name === 'mobile'}, false)`);
  // 侧栏宽度有 .3s 过渡：不等它走完就拍，紧接着的空态镜头会把半展开的侧栏
  // 定格成 117–167px（2026-11-27 实录，main 与 W6-1 同样中招）。等宽度连续
  // 两次读数相同，再等过渡收尾（W6-8：轨的竖排 .side-rail 在过渡结束才换上，
  // 收起前 wide 内容先淡出）——不写死目标宽度，先空等一帧再读，免得过渡
  // 还没起步就被判成「已稳定」。
  await delay(60);
  let prev = -1;
  for (let i = 0; i < 40; i++) {
    const w = await evalJS(`document.getElementById('side').getBoundingClientRect().width`);
    if (w === prev) break;
    prev = w;
    await delay(40);
  }
  for (let i = 0; i < 20 && await evalJS(`document.body.classList.contains('side-fading')`); i++) await delay(50);
  return;
  throw new Error(`侧栏宽度 2s 内未落定（最后一次读数 ${prev}px）`);
}
async function until(what, expr, ms = 8000) {
  const t0 = Date.now();
  for (;;) {
    let v = false;
    try { v = await evalJS(expr); } catch { /* 渲染中途的瞬时态，继续 */ }
    if (v) return;
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(120);
  }
}
const send = text => evalJS(
  `fetch(q('/msg'), { method: 'POST',
     headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
     body: JSON.stringify({ text: ${JSON.stringify(text)} }) }).then(r => r.status)`);

// 到聊天面（侧栏在、无面板开、无会话正文）：有面板开着点它的钮收回，
// 会话正文开着点 💬 返回。
async function backToChat() {
  await evalJS(`(() => {
    const cl = document.body.classList;
    for (const c of ['mem-view','stat-view','agents-view','settings-view','hooks-view']) {
      if (cl.contains(c)) {
        const map = { 'mem-view':'mem-btn','stat-view':'stat-btn','agents-view':'agents-btn',
                      'settings-view':'set-btn','hooks-view':'hk-btn' };
        document.getElementById(map[c]).click();
        return;
      }
    }
    if (cl.contains('viewing')) viewLive();   // 页面上下文：直接调页面函数
  })()`);
  await delay(150);
}

// 单面板导航：pane 型走按钮（togglePane 自带互斥），sidebar/chat-rich 走
// 专属路径。返回 {ok, error}——异常是审查发现，镜头照拍。
async function navigate(panel) {
  if (panel.key === 'sidebar') {
    await backToChat();
    await evalJS(`${$id('side-search')}.focus()`);
    await until('侧栏会话行', `document.querySelectorAll('#sess-list .sess-row').length >= 3`);
    return {};
  }
  if (panel.key === 'chat-rich') {
    await backToChat();
    if (!RICH_SID) throw new Error('未传 --rich-sid');
    await evalJS(`document.querySelector('.sess-row[data-sid="${RICH_SID}"]').click()`);
    // 就绪条件跟页面的对话流形态走：W6-3 起历史由 histTurn 渲染（.msg / .tcall），
    // W6-5 起落在 #hist——驱动还等旧载体的类名/容器（.smsg 与当时的会话正文容器）就是等
    // 8 秒超时后硬拍，「富会话」镜头全没走完导航（W6-3/W6-4 的证据即如此，
    // 2026-11-27 修）。
    await until('会话正文（W6-5 #hist）', `document.querySelectorAll('#hist .msg').length >= 4`);
    // 展开第一行工具调用：参数/结果面板进 DOM（懒建），夹具的「工具行」
    // 易烂元素才算进了镜头。
    await evalJS(`(() => {
      const row = document.querySelector('#hist .tcall');
      if (row) row.click();
      return !!row;
    })()`);
    await until('工具调用展开', `document.querySelectorAll('#hist .tdetail:not([hidden])').length >= 1`);
    return {};
  }
  // pane 型：已开则不点（togglePane 是开关，二连点会关掉）。按钮点了但
  // 面板类 2.5s 内没挂上 = 按钮没接线（2026-11-22 首跑实测：hkBtn 缺 click
  // 监听；c_25d2ey 已补回并加 hooksaudit 无头回归）——驱动仍直接挂类 +
  // 调 load 函数把面板本体强制亮出来保住「面板可审」，按钮失效作为
  // navError 记进 manifest（审查发现，不是驱动故障）；这条 fallback 只
  // 用于取证，不当作绿灯。
  const open = await evalJS(`document.body.classList.contains('${panel.cls}')`);
  // 设置是模态（W6-6）：开着就别再点（#set-btn 对已开模态是关闭）。
  if (!open) await evalJS(`${$id(panel.btn)}.click()`);
  let navErr = null;
  try {
    await until(`面板类 ${panel.cls}`, `document.body.classList.contains('${panel.cls}')`, 2500);
  } catch {
    await evalJS(`(() => {
      closePanes();   // 页面上下文：直接调页面函数
      if ('${panel.cls}' === 'settings-view') { openSettings(); return; }   // W6-6 模态
      document.body.classList.add('${panel.cls}', 'browsing');
      ${panel.loadFn}();
    })()`);
    navErr = `pane 按钮 ${panel.btn} 点击无效（已由驱动强制加载面板取证）——疑似按钮监听缺失`;
  }
  // 设置镜头取「服务配置」tab：dsh-06 对照的是行式表单本体，不是外观三选。
  if (panel.key === 'settings') {
    await evalJS(`(() => {
      const b = document.querySelector('.set-tab[data-tab="serve"]');
      if (b) b.click();
    })()`);
  }
  const readyExpr = {
    memory: `document.querySelectorAll('#mem-list .mem-row').length >= 1`,
    stats: `${$id('stat-body')}.children.length >= 1`,
    agents: `${$id('ag-list')}.children.length >= 1`,
    settings: `${$id('set-body')}.children.length >= 2`,
    hooks: `document.querySelectorAll('#hk-log .hk-row').length >= 1`
  }[panel.key];
  await until(panel.label + ' 数据落 DOM', readyExpr);
  return navErr ? { error: navErr } : {};
}

async function shot(seqNo, panelKey, label, theme, view) {
  phase = `${seqNo}-${panelKey}-${theme}-${view.name}`;
  const file = `${String(seqNo).padStart(2, '0')}-${panelKey}-${theme}-${view.name}.png`;
  const entry = { seq: seqNo, file, panel: panelKey, theme, width: view.width, height: view.height };
  try {
    const r = await call('Page.captureScreenshot',
      { format: 'png', captureBeyondViewport: true });
    writeFileSync(path.join(outDir, file), Buffer.from(r.data, 'base64'));
    entry.ok = true;
  } catch (e) {
    entry.ok = false;
    entry.error = 'capture: ' + e.message;
  }
  shotMeta.push(entry);
  console.log(`  ${entry.ok ? 'shot' : 'FAIL'}  ${file}`);
  return entry;
}

// W6-7 资产面板形状探针（只记录不拦，进 manifest.panel_shape）：点一行看
// 列表 / 详情的几何——桌面（≥1200）= 列表 320 + 详情并排、合计 768；窄屏 =
// 详情替换列表、出「返回列表」；再点返回验回列表。探完清选中，镜头不受影响。
// 硬断言在 web-subagents 无头（同一套判据，那边判失败）。
async function panelShape(key) {
  const ids = key === 'memory'
    ? ['memory', 'mem-list', 'mem-detail', '.mem-row'] : ['agents', 'ag-list', 'ag-detail', '.ag-row'];
  return evalJS(`(async () => {
    const sec = document.getElementById('${ids[0]}'), list = document.getElementById('${ids[1]}'),
      det = document.getElementById('${ids[2]}'), row = list.querySelector('${ids[3]}');
    const vis = e => getComputedStyle(e).display !== 'none';
    if (!row) return { ok: null, why: '列表无行' };
    const body = list.parentElement, pad = parseFloat(getComputedStyle(body).paddingLeft);
    const col = body.clientWidth - 2 * pad, wide = innerWidth >= 1200;
    row.click(); await new Promise(r => setTimeout(r, 500));
    const back = det.querySelector('.pane-back');
    const picked = sec.classList.contains('has-sel') && vis(det) && (wide
      ? vis(list) && Math.round(list.getBoundingClientRect().width) === 320
        && Math.round(det.getBoundingClientRect().width) === col - 336 && !vis(back)
      : !vis(list) && Math.round(det.getBoundingClientRect().width) === col && vis(back));
    back.click(); await new Promise(r => setTimeout(r, 300));
    const restored = !sec.classList.contains('has-sel') && vis(list) && !vis(det);
    return { ok: picked && restored, wide, col, picked, restored };
  })()`);
}

// ---- 4. Phase A：聊天空态（/bind 前）× 主题 × 视口 ----
for (const theme of THEMES) {
  phase = `A-chat-empty-${theme}`;
  await setTheme(theme);
  for (const view of VIEWS) {
    await setViewport(view);
    await shot(shotMeta.length + 1, 'chat-empty', '聊天空态', theme, view);
  }
}

// ---- 5. Phase B：/bind 桩项目（后台/记忆面板的绑定前提）----
phase = 'B-bind';
if ((await send(`/bind ${projDir}`)) !== 202) {
  console.error('FAIL: /bind 非 202');
  process.exit(1);
}
await until('绑定应答', `document.querySelector('#list .msg:last-child')?.textContent.includes('已绑定项目')`, 10_000);

// ---- 6. Phase C：7 面板 × 主题 × 视口；富会话亮色桌面顺带 DOM dump ----
let domDumped = false;
for (const theme of THEMES) {
  await setTheme(theme);
  for (const view of VIEWS) {
    await setViewport(view);
    for (const panel of PANELS) {
      const seqNo = shotMeta.length + 1;
      phase = `C-${seqNo}-${panel.key}-${theme}-${view.name}`;
      let nav = {};
      try {
        nav = await navigate(panel);
      } catch (e) {
        nav = { error: e.message };   // 渲染异常 = 审查发现，镜头照拍
        console.log(`  warn  ${panel.label}（${theme}/${view.name}）：${e.message}`);
      }
      const entry = await shot(seqNo, panel.key, panel.label, theme, view);
      if (nav.error) entry.navError = nav.error;
      if (panel.key === 'memory' || panel.key === 'agents') {
        try { entry.shape = await panelShape(panel.key); } catch (e) { entry.shape = { ok: null, why: e.message }; }
      }
      if (panel.key === 'chat-rich' && theme === 'light' && view.name === 'desktop' && !domDumped) {
        const html = await evalJS('document.documentElement.outerHTML');
        writeFileSync(path.join(outDir, 'dom-chat-rich.html'), '<!DOCTYPE html>\n' + html);
        domDumped = true;
      }
    }
  }
}

// ---- 6.5 视觉趋势计数：五项指标（只记录不判失败）----
// 读的是「serve 出去的页面源」而不是渲染后的 DOM：要数的是源里的 <style>
// 与 :root token，渲染态会把 var() 展开成计算值、趋势就失真了。采集失败
// 只记 manifest.visual.error，绝不折损退出码。
const CONTRAST_FGS = ['fg', 'fg-dim', 'fg-soft', 'ok', 'warn', 'err', 'link'];
const CONTRAST_BGS = ['inset', 'panel', 'soft', 'accent'];

async function fetchPageSource() {
  const r = await fetch(pageUrl, { signal: AbortSignal.timeout(10_000) });
  if (!r.ok) throw new Error(`GET 页面源 ${r.status}`);
  return r.text();
}

// WCAG 2.2 相对亮度比：(L亮 + .05) / (L暗 + .05)
const srgbToLinear = c => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const relLuminance = hex => {
  const [r, g, b] = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255);
  return 0.2126 * srgbToLinear(r) + 0.7152 * srgbToLinear(g) + 0.0722 * srgbToLinear(b);
};
const contrastRatio = (a, b) => {
  const [x, y] = [relLuminance(a), relLuminance(b)];
  return +(((Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)).toFixed(2));
};

function visualMetrics(html) {
  // 先剥 HTML 注释再抽 <style>：页面上方的注释正文里提过 "<style>" 字样，
  // 不剥会把注释到真 </style> 之间整段吞成幻影块（webfrontend_static.go 的
  // styleBlockRE 注释记过同款坑——那边靠行锚定解决，这里两道都上）。
  const doc = html.replace(/<!--[\s\S]*?-->/g, '');
  const styleBlocks = [...doc.matchAll(/^<style\b[^>]*>([\s\S]*?)<\/style>/gim)].map(m => m[1]);
  // 注释里的「样例色 / 示例字号」不是真用法，CSS/JS 块注释统一剥掉。
  const css = styleBlocks.join('\n').replace(/\/\*[\s\S]*?\*\//g, '');

  // :root 两套 token：暗 = 裸 :root，亮 = :root[data-theme="light"]。
  const tokenSets = {};
  for (const m of css.matchAll(/:root\s*(\[[^\]]*\])?\s*\{([^}]*)\}/g)) {
    const theme = m[1] && m[1].includes('light') ? 'light' : 'dark';
    const toks = {};
    for (const d of m[2].matchAll(/--seek-([a-z0-9-]+)\s*:\s*([^;]+);/gi)) toks[d[1]] = d[2].trim();
    tokenSets[theme] = toks;
  }
  // 一层别名解析（暗色 --seek-link: var(--seek-ok)）；解析后不是 #rrggbb 的跳过。
  const resolve = (toks, name) => {
    let v = toks[name];
    for (let i = 0; v !== undefined && i < 3; i++) {
      const ref = v.match(/^var\(--seek-([a-z0-9-]+)\)$/i);
      if (!ref) break;
      v = toks[ref[1]];
    }
    return v && /^#[0-9a-f]{6}$/i.test(v) ? v : null;
  };

  const lowContrast = {
    threshold: VISUAL_THRESHOLDS.minContrast,
    checked_pairs: CONTRAST_FGS.length * CONTRAST_BGS.length,
  };
  for (const theme of ['dark', 'light']) {
    const toks = tokenSets[theme];
    if (!toks || !Object.keys(toks).length) {
      lowContrast[theme] = { error: '--seek-* token 未解析到' };
      continue;
    }
    const pairs = [];
    for (const fg of CONTRAST_FGS) for (const bg of CONTRAST_BGS) {
      const f = resolve(toks, fg), b = resolve(toks, bg);
      if (!f || !b) continue;
      const ratio = contrastRatio(f, b);
      if (ratio < VISUAL_THRESHOLDS.minContrast) {
        pairs.push({ fg: `--seek-${fg}`, bg: `--seek-${bg}`, ratio });
      }
    }
    pairs.sort((a, b) => a.ratio - b.ratio);
    lowContrast[theme] = { count: pairs.length, pairs };
  }

  const values = re => [...new Set([...css.matchAll(re)].map(m => m[1]))];
  const numeric = vs => vs.map(Number).sort((a, b) => a - b);
  const fontSizes = numeric(values(/font-size:\s*([\d.]+)px/gi));
  const fontWeights = values(/font-weight:\s*([\w.]+)/gi).sort();
  const radii = numeric(values(/border-radius:\s*([\d.]+)px/gi));

  // :root 外 hex：剥掉两套 token 块后剩余的字面量（去重）。2026-11-24 基线
  // 表记的 31 是「全文件去重数」——当时 31 个 hex 恰好全住在 :root 里，故本
  // 指标按字面算法得 0；两个数都落盘，免得被读成「31 处硬编码散落全文件」。
  // 这里不剥 CSS 注释：注释里点名的色值也算「字面量」（31 里就含一个）。
  const hexes = txt => [...new Set([...txt.matchAll(/#[0-9a-fA-F]{6}\b/g)].map(m => m[0].toLowerCase()))];
  const hexOutside = hexes(doc.replace(/:root\s*(?:\[[^\]]*\])?\s*\{[^}]*\}/g, ''));

  return {
    low_contrast_pairs: lowContrast,
    font_sizes_px: { count: fontSizes.length, values: fontSizes },
    font_weights: { count: fontWeights.length, values: fontWeights },
    hex_outside_root: { count: hexOutside.length, values: hexOutside,
      distinct_in_file: hexes(doc).length },
    border_radii_px: { count: radii.length, values: radii },
  };
}

let visual;
try {
  visual = visualMetrics(await fetchPageSource());
} catch (e) {
  visual = { error: String((e && e.message) || e) };
  console.log(`  warn  visual 计数失败（不影响退出码）：${visual.error}`);
}

// ---- 7. 产物：console.txt + manifest.json ----
writeFileSync(path.join(outDir, 'console.txt'),
  consoleLog.length === 0 ? '（全程零 console 输出/异常/资源错误）\n'
    : consoleLog.map(e => `[${e.phase}] [${e.kind}] ${e.text}`).join('\n') + '\n');

const host = new URL(pageUrl).host;   // manifest 不落 token
writeFileSync(path.join(outDir, 'manifest.json'), JSON.stringify({
  tool: 'scripts/web-page-review.sh + scripts/web-page-review-headless.mjs',
  card: 'c_vcnacz',
  generated_at: new Date().toISOString(),
  git_rev: process.env.PAGE_REVIEW_GIT_REV || 'unknown',
  page_host: host,
  sandbox: {
    seek_home: process.env.PAGE_REVIEW_HOME || '(shell 侧事实，见 isolation.txt)',
    bound_project: projDir,
    port: new URL(pageUrl).port,
    prod_8790_avoided: new URL(pageUrl).port !== '8790',
  },
  matrix: { themes: THEMES, views: VIEWS, panels: ['chat-empty', ...PANELS.map(p => p.key)] },
  expected_shots: 32,
  shots: shotMeta,
  nav_errors: shotMeta.filter(s => s.navError).length,
  capture_failures: shotMeta.filter(s => !s.ok).length,
  panel_shape: shotMeta.filter(s => s.shape).map(s => ({ file: s.file, width: s.width, ...s.shape })),
  console_entries: consoleLog.length,
  dom_dump: domDumped ? 'dom-chat-rich.html' : null,
  visual,
  reading_note: 'console 与 navError 是审查证据不是脚本失败：坏页面正是本工具要抓的东西；'
    + '退出码只看镜头是否拍齐（32/32）。visual 五项是趋势指标，只记录不拦。',
}, null, 2) + '\n');

{
  const sh = shotMeta.filter(s => s.shape);
  const bad = sh.filter(s => s.shape.ok !== true);
  console.log(`\npanel_shape（W6-7，只记录）：${sh.length - bad.length}/${sh.length} 过`
    + (bad.length ? '——未过：' + bad.map(s => `${s.file} ${JSON.stringify(s.shape)}`).join('；') : ''));
}
if (visual.error) console.log(`\nvisual: 采集失败（${visual.error}）——不影响退出码`);
else console.log('\nvisual: '
  + `低对比 暗${visual.low_contrast_pairs.dark.count ?? '?'}/亮${visual.low_contrast_pairs.light.count ?? '?'} 对`
  + ` · 字号 ${visual.font_sizes_px.count} 档 · 字重 ${visual.font_weights.count} 档`
  + ` · :root 外 hex ${visual.hex_outside_root.count} · 圆角 ${visual.border_radii_px.count} 档`);

const ok = shotMeta.length === 32 && shotMeta.every(s => s.ok);
console.log(ok ? '\n32 镜头拍齐（证据包齐：manifest + PNG + console + DOM dump）'
  : `\nFAIL：镜头 ${shotMeta.length}/32（capture 失败 ${shotMeta.filter(s => !s.ok).length}）`);
process.exit(ok ? 0 : 1);
