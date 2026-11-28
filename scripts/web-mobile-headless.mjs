#!/usr/bin/env node
// web-mobile-headless.mjs — 390px 手机宽响应式的无头验收半边
// （卡 c_lqtt2m；由 scripts/web-mobile-manual.sh 驱动，参照
// web-theme-headless.mjs 的零依赖 CDP 引导）。
//
// 断言景（对应卡面验收①「390px 全页面无不可达入口/不可读裁切」）：
//   单栏   → ≤480px 缺省收起（44px 轨在场）+ 展开态抽屉化：
//            #side 计算样式 position=absolute、遮罩可见；
//   轨可达 → 收起轨 44px 装得下 #side-head 的全部图标钮（＋/🗄/⟩）：
//            每个钮的右缘不越轨、中心点 hit-test 命中它自己（返工轮补——
//            overflow 溢出轨压到中栏时，会话详情页的 ⟩ 被「💬 聊天」盖住，
//            rect 断言看不见，只有 elementFromPoint 会红）；
//   视图钮（#side-nav，W6-2 从 #bar 搬进侧栏）→ 展开抽屉内完整在视口内（
//            无自身横向裁切），/cancel 全文可见；
//   ellipsis → 每个面 documentElement.scrollWidth ≤ innerWidth
//            （页面级零横向溢出）；设置面板 .set-row 纵向堆叠
//            （computed flexDirection=column）、长路径值带 ellipsis；
//   资产面 → 记忆/后台列表列纵向堆叠（不再 320/380px 挤没详情列）；
//   截图   → 两主题 × 聊天/抽屉/会话正文/设置/记忆/统计/后台/审计。
//
// 用法：node web-mobile-headless.mjs <页面URL> <截图输出目录> [--chrome <路径>]
//                                   [--cdp-port <端口>]
// 退出码：0 全绿；1 失败。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { disposeHeadlessChrome } from './web-headless-chrome.mjs';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const argVal = flag => { const i = argv.indexOf(flag); return i > 0 ? argv[i + 1] : null; };
const CHROME = argVal('--chrome') || process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
// CDP 调试口默认 18813：避开既有手测脚本的 18790–18798 与
// web-manual-nontty-test 的 18820/18900 区间（同机并行不互抢）。
const dbgPort = parseInt(argVal('--cdp-port') || process.env.CDP_PORT || '18813', 10);
const rest = argv.filter((a, i) => !a.startsWith('--') && !(i > 0 && argv[i - 1].startsWith('--')));
const pageUrl = rest[0];
const outDir = rest[1];
if (!pageUrl || !outDir) {
  console.error('usage: web-mobile-headless.mjs <pageUrl> <outDir> [--chrome path] [--cdp-port n]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket)');
  process.exit(1);
}
const WATCHDOG_MS = 180_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. headless Chrome（窗口尺寸会被设备模拟覆盖，仅占位）----
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5mobile-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', '--window-size=390,844',
  `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, 'about:blank',
], { stdio: ['ignore', 'ignore', 'pipe'] });
trace(`chrome pid=${chrome.pid} 起动 (cdp ${dbgPort})`);
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

// ---- 2. 390×844 @2x 设备模拟 + 打开页面 ----
await cdp('Page.enable');
await cdp('Emulation.setDeviceMetricsOverride',
  { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
await cdp('Page.navigate', { url: pageUrl });
await delay(1500);   // 等 SSE 首拉 + 首帧稳定

// ---- 3. 断言与截图 ----
let fails = 0;
function ck(name, ok, detail) {
  if (ok) { console.log(`  ok  ${name}`); }
  else { fails++; console.log(`  FAIL ${name}${detail ? '：' + detail : ''}`); }
}
async function shot(name) {
  const r = await cdp('Page.captureScreenshot', { format: 'png' });
  writeFileSync(path.join(outDir, name + '.png'), Buffer.from(r.data, 'base64'));
  console.log(`  shot ${name}.png`);
}
// 页面级横向溢出 + 可见元素级横向裁切（内容比盒子宽且无横向滚动策略）
const OVERFLOW_JS = `(() => {
  const vw = window.innerWidth;
  const doc = document.documentElement.scrollWidth - vw;
  const clipped = [];
  for (const el of document.querySelectorAll('button, .k, .v, #status-l, .t1')) {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') continue;
    const r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) continue;
    const off = el.scrollWidth - el.clientWidth;
    const cutsRight = r.right > vw + 1;
    if (cutsRight || (off > 1 && cs.overflowX !== 'auto' && cs.overflowX !== 'scroll'
        && cs.textOverflow !== 'ellipsis' && cs.whiteSpace !== 'normal'))
      clipped.push(el.id || el.className + ':' + (el.textContent || '').slice(0, 12)
        + (cutsRight ? '(越视口' + Math.round(r.right - vw) + 'px)' : '(裁' + off + 'px)'));
  }
  return { vw, doc, clipped: clipped.slice(0, 8) };
})()`;
// 收起轨可达性（c_lqtt2m 返工轮）：44px 轨必须装得下 #side-head 的全部
// 图标钮，且每个钮中心点的 hit-test 仍是它自己。溢出轨的几何在 rect 层面
// 看不出来（钮本身宽度没变），只有 elementFromPoint 抓得住「被相邻列的
// 💬 聊天 按钮压住、点不到」——这正是返工轮判定的不过项。
const RAIL_JS = `(() => {
  const side = document.getElementById('side');
  const sr = side.getBoundingClientRect();
  const bad = [];
  for (const b of document.querySelectorAll('#side-head button, #side-new, #side-sec button, #side-nav button')) {
    const r = b.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) { bad.push(b.id + '(不可见)'); continue; }
    if (r.right > sr.right + 1 || r.left < sr.left - 1)
      bad.push(b.id + '(溢出轨 ' + Math.round(r.right - sr.right) + 'px)');
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    if (!hit || !(hit === b || b.contains(hit)))
      bad.push(b.id + '(被 ' + ((hit && (hit.id || hit.tagName)) || '空') + ' 盖住)');
  }
  return bad;
})()`;

for (const theme of ['dark', 'light']) {
  console.log(`== ${theme} @390px ==`);
  await evalJS(`try { localStorage.setItem('seek-theme', '${theme}'); } catch (e) {}`);
  await cdp('Page.reload', {});
  await delay(1200);

  // 单栏缺省：390 < 1024 → 收起轨
  ck(`${theme} 缺省收起（44px 轨）`,
    await evalJS(`document.body.classList.contains('side-collapsed')`));
  const rail1 = await evalJS(RAIL_JS);
  ck(`${theme} 收起轨图标钮全在轨内且点得到`, rail1.length === 0, rail1.join('；'));
  const o1 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 聊天面零横向溢出`, o1.doc <= 0, 'doc=' + o1.doc);
  ck(`${theme} 聊天面无裁切元素`, o1.clipped.length === 0, o1.clipped.join('；'));
  const barFit = await evalJS(`(() => {
    const bad = [];
    for (const b of document.querySelectorAll('#side-nav button, #side-new')) {
      const r = b.getBoundingClientRect();
      if (r.right > window.innerWidth + 1) bad.push(b.id + '→' + Math.round(r.right));
      if (b.scrollWidth > b.clientWidth + 1) bad.push(b.id + '(裁' + (b.scrollWidth - b.clientWidth) + 'px)');
    }
    return bad;
  })()`);
  ck(`${theme} 按钮组全显（含 /cancel 全文）`, barFit.length === 0, barFit.join('；'));
  await shot(`${theme === 'dark' ? '1' : '2'}1-chat`);

  // 抽屉化：展开 → absolute + 遮罩
  await evalJS(`document.getElementById('side-toggle').click()`);
  await delay(400);
  const drawer = await evalJS(`(() => {
    const s = getComputedStyle(document.getElementById('side'));
    const sc = document.getElementById('side-scrim');
    return { pos: s.position, w: Math.round(parseFloat(s.width)),
             scrim: getComputedStyle(sc).display,
             chatW: Math.round(document.getElementById('center').getBoundingClientRect().width) };
  })()`);
  ck(`${theme} 侧栏抽屉化（absolute 覆盖）`, drawer.pos === 'absolute',
    'pos=' + drawer.pos + ' w=' + drawer.w);
  ck(`${theme} 遮罩出场`, drawer.scrim === 'block', 'display=' + drawer.scrim);
  ck(`${theme} 中栏恒满宽（≈390）`, drawer.chatW >= 385 && drawer.chatW <= 390,
    'chatW=' + drawer.chatW);
  await shot(`${theme === 'dark' ? '1' : '2'}2-drawer`);
  const o2 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 抽屉面零横向溢出`, o2.doc <= 0, 'doc=' + o2.doc);

  // 遮罩点击关抽屉
  await evalJS(`document.getElementById('side-scrim').click()`);
  await delay(300);
  ck(`${theme} 遮罩点击收抽屉`,
    await evalJS(`document.body.classList.contains('side-collapsed')`));

  // 会话正文（含代码块）：展开抽屉 → 点会话行 → 抽屉自动收
  await evalJS(`document.getElementById('side-toggle').click()`);
  await delay(300);
  const closed = await evalJS(`(() => {
    const row = document.querySelector('.sess-row');
    if (!row) return 'no-row';
    row.click();
    return new Promise(r => setTimeout(() => r(
      document.body.classList.contains('side-collapsed') &&
      document.body.classList.contains('viewing') ? 'ok'
      : 'state=' + document.body.className), 600));
  })()`);
  ck(`${theme} 选中会话自动收抽屉`, closed === 'ok', closed);
  await delay(600);   // 等正文拉完
  // 返工轮现场：会话详情页收起态，⟩ 与「💬 聊天」同排抢位
  const rail2 = await evalJS(RAIL_JS);
  ck(`${theme} 会话详情页收起轨点得到（不被 💬 聊天 盖住）`,
    rail2.length === 0, rail2.join('；'));
  const o3 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 会话正文零横向溢出`, o3.doc <= 0, 'doc=' + o3.doc);
  ck(`${theme} 会话正文无裁切元素`, o3.clipped.length === 0, o3.clipped.join('；'));
  // 代码块协同：夹具正文带围栏长行——pre 自己当横滚容器（长行滚着看，
  // 不越气泡被转录区硬裁）。首测这里靠目检抓到 .smsg pre 缺规则。
  const pre = await evalJS(`(() => {
    const p = document.querySelector('#hist .msg pre');
    return p ? { ox: getComputedStyle(p).overflowX,
                 scrolls: p.scrollWidth > p.clientWidth } : 'no-pre';
  })()`);
  ck(`${theme} 会话正文代码块自持横滚`, pre && pre !== 'no-pre' && pre.ox === 'auto',
    JSON.stringify(pre));
  await shot(`${theme === 'dark' ? '1' : '2'}3-sess`);
  await evalJS(`viewLive()`);
  await delay(300);

  // 设置面板：W6-6 起是模态（遮罩 + 居中框 + 左 tab）。窄屏口径：对话框满宽
  // 减 32、tab 栏横排成顶部一条、行式表单纵向堆叠、长路径按 ellipsis 折。
  await evalJS(`document.getElementById('set-btn').click()`);
  await delay(400);
  // 设置是模态（W6-6）：遮罩盖住整页，侧栏轨「点不到」正是模态语义——
  // 旧的「设置页收起轨点得到」断言随之退役，改验遮罩真的把轨挡住了。
  const rail3 = await evalJS(`(() => {
    const side = document.getElementById('side');
    const r = side.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + 8, r.top + 8);
    return hit ? (hit.id || hit.tagName) : '';
  })()`);
  ck(`${theme} 设置模态遮罩盖住整页（侧栏不可点）`, rail3 === 'set-scrim' || rail3 === 'settings',
    'hit=' + rail3);
  // 切到「服务配置」tab 才有值行（通用 tab 只有外观三选控件）。
  await evalJS(`document.querySelector('.set-tab[data-tab="serve"]').click()`);
  await delay(400);
  const modal = await evalJS(`(() => {
    const m = document.getElementById('settings');
    const r = m.getBoundingClientRect();
    return { w: Math.round(r.width), vw: innerWidth,
      tabsRow: getComputedStyle(document.getElementById('set-tabs')).flexDirection,
      scrim: getComputedStyle(document.getElementById('set-scrim')).display };
  })()`);
  ck(`${theme} 390px 对话框满宽减 32`, Math.abs(modal.w - (modal.vw - 32)) <= 2,
    `w=${modal.w} vw=${modal.vw}`);
  ck(`${theme} 390px tab 栏横排（顶部一条）`, modal.tabsRow === 'row', modal.tabsRow);
  ck(`${theme} 390px 遮罩可见`, modal.scrim !== 'none', modal.scrim);
  const setStat = await evalJS(`(() => {
    const row = document.querySelector('.set-row');
    const vs = document.querySelectorAll('.set-row .v');
    let ell = null;
    for (const v of vs) {
      if ((v.textContent || '').includes('/')) { ell = {
        text: v.textContent.slice(0, 16), cs: getComputedStyle(v) }; break; }
    }
    return { dir: row ? getComputedStyle(row).flexDirection : 'none',
             vEll: ell ? (ell.cs.textOverflow === 'ellipsis' &&
                          ell.cs.overflow === 'hidden') : null };
  })()`);
  ck(`${theme} 设置行纵向堆叠`, setStat.dir === 'column', 'dir=' + setStat.dir);
  ck(`${theme} 长路径值按 ellipsis 折`, setStat.vEll === true, 'vEll=' + setStat.vEll);
  const o4 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 设置面零横向溢出`, o4.doc <= 0, 'doc=' + o4.doc);
  ck(`${theme} 设置面无裁切元素`, o4.clipped.length === 0, o4.clipped.join('；'));
  await shot(`${theme === 'dark' ? '1' : '2'}4-settings`);
  // 关掉模态再看后面的面板（不看就会连带拍进遮罩）。
  await evalJS(`document.getElementById('set-close').click()`);
  await delay(300);

  // 记忆 / 统计 / 后台 / 审计
  await evalJS(`document.getElementById('mem-btn').click()`);
  await delay(500);
  ck(`${theme} 记忆列纵向堆叠`,
    await evalJS(`getComputedStyle(document.getElementById('mem-body')).flexDirection`) === 'column');
  const o5 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 记忆面零横向溢出`, o5.doc <= 0, 'doc=' + o5.doc);
  await shot(`${theme === 'dark' ? '1' : '2'}5-memory`);

  await evalJS(`document.getElementById('stat-btn').click()`);
  await delay(400);
  const o6 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 统计面零横向溢出`, o6.doc <= 0, 'doc=' + o6.doc);
  await shot(`${theme === 'dark' ? '1' : '2'}6-stats`);

  await evalJS(`document.getElementById('agents-btn').click()`);
  await delay(400);
  ck(`${theme} 后台列纵向堆叠`,
    await evalJS(`getComputedStyle(document.getElementById('agents-body')).flexDirection`) === 'column');
  const o7 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 后台面零横向溢出`, o7.doc <= 0, 'doc=' + o7.doc);
  await shot(`${theme === 'dark' ? '1' : '2'}7-subagents`);

  await evalJS(`document.getElementById('hk-btn').click()`);
  await delay(400);
  const o8 = await evalJS(OVERFLOW_JS);
  ck(`${theme} 审计面零横向溢出`, o8.doc <= 0, 'doc=' + o8.doc);
  await shot(`${theme === 'dark' ? '1' : '2'}8-hooks`);
}

console.log(fails === 0 ? '无头验收全绿（截图见 ' + outDir + '）'
  : `FAIL：${fails} 项断言未过`);
process.exit(fails === 0 ? 0 : 1);
