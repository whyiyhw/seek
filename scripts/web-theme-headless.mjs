#!/usr/bin/env node
// web-theme-headless.mjs — 主题切换 + 设置面板的无头浏览器验收半边
// （scripts/web-theme-manual.sh 的驱动；W5 卡 c_ni7mzw 验收①–③）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动
// 本机 headless Chrome。全部交互走真实 UI 路径（元素 .click()——与真人
// 操作同一条线），脚本不持有 token（URL 由外层脚本传入）。
//
// 断言景（对应 manual 清单）：
//   初始     → 无记忆位时 data-theme 跟随 emulated prefers-color-scheme
//              （显式模拟成 light：确定性，不赌 headless 缺省值）
//   防FOUC   → 记忆位 light / dark 两方向各 reload 一次，用
//              Page.addScriptToEvaluateOnNewDocument 在任何页面脚本之前挂
//              首帧采样器（首个 rAF 里读 data-theme + body 底色）：首帧
//              就是目标主题——dark 刷新首帧不白、light 刷新首帧不黑。
//              bootstrap 在 <style> 之前同步跑完，根本没有过渡帧可闪
//              （源码面由 TestWeb_PageThemeSettings 钉住顺序）。
//   设置面板 → 只读口径落实：serve 侧配置每行值钉住（fixture 全非默认：
//              provider=deepseek/并发 6/yolo 开/监听 ADDR/压缩 350000
//              tokens/项目 1 条）、模型与温度行明写「无配置项」、关于区
//              版本 + token …后 6 位；面板内零写入控件（无 input/select/
//              textarea，button 仅主题三枚）；「编辑 config.json 后重启」
//              文案在场。
//   对比度   → 两主题 × 七个面（会话侧栏/状态栏/输入语音含录音态/设置/
//              会话正文/记忆/统计/后台）：对每个可见文字元素取 computed
//              color + 向上冒泡的有效底色，逐对算 WCAG 对比度，全 ≥4.5:1。
//   系统跟随 → 清记忆位点「跟随系统」，Emulation.setEmulatedMedia 把系统
//              外观 dark↔light 来回翻，data-theme 实时跟随（不用刷新）。
//
// 用法：node scripts/web-theme-headless.mjs <页面URL> <token尾6位> <项目路径> <会话sid>
//       [--chrome <chrome 可执行文件>]（默认 CHROME env 或 macOS 标准位）
// 退出码：0 全部断言过；1 启动/断言失败。
import { spawn } from 'node:child_process';
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
const TOK_TAIL = args[1];
const PROJ = args[2];
const SID = args[3];
if (!pageUrl || !TOK_TAIL || !PROJ || !SID) {
  console.error('usage: web-theme-headless.mjs <pageUrl> <tokenTail> <proj> <sid> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const WATCHDOG_MS = 180_000;
// 总闸 watchdog：任何一景卡死都不许把脚本挂成无限等——超时退出码 1。
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18797;   // 侧栏脚本用 18793、md 用 18794…，错开免互抢
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5theme-chrome.'));
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

// ---- 2. CDP：Runtime.evaluate + 通用调用（reload / 首帧注入 / 媒体模拟）----
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
async function cdp(method, params) {
  return new Promise((res, rej) => {
    const id = ++seq;
    pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method, params: params || {} }));
  });
}
// 模拟系统外观（prefers-color-scheme）；无参调用清除模拟。
const emulateScheme = scheme => cdp('Emulation.setEmulatedMedia',
  { features: scheme ? [{ name: 'prefers-color-scheme', value: scheme }] : [] });

// 首帧采样器：挂在每个新文档最前（早于页面一切脚本），首个 rAF 回调里
// 读 data-theme 与 body 计算底色——防 FOUC 的运行时证据：记忆位=dark 时
// 首帧就得是 dark，不存在「先画暗再翻亮」的中间帧。
const FIRST_FRAME_SRC = `(function(){
  var n = 0;
  function grab(){
    if (!document.body) { if (++n < 20) requestAnimationFrame(grab); return; }
    if (window.__firstFrame) return;
    window.__firstFrame = {
      theme: document.documentElement.getAttribute('data-theme'),
      bg: getComputedStyle(document.body).backgroundColor
    };
  }
  requestAnimationFrame(grab);
})();`;
await cdp('Page.enable');   // addScriptToEvaluateOnNewDocument 属 Page 域：不 enable 注入不生效（首帧采样器全空）
await cdp('Page.addScriptToEvaluateOnNewDocument', { source: FIRST_FRAME_SRC });
const reload = () => cdp('Page.reload', {});

// ---- 3. 页面侧采样器（对比度对收集；函数整体内联进表达式）----
const SAMPLE_JS = `(rootSel) => {
  function parse(c){ const m = c.match(/rgba?\\(([^)]+)\\)/); if (!m) return null;
    const p = m[1].split(',').map(Number); return { r:p[0], g:p[1], b:p[2], a:p.length>3?p[3]:1 }; }
  function lum(c){ const x = parse(c); if (!x) return 1;
    const f = v => { v/=255; return v<=0.03928 ? v/12.92 : Math.pow((v+0.055)/1.055, 2.4); };
    return (0.2126*f(x.r)+0.7152*f(x.g)+0.0722*f(x.b)) * x.a; }
  function contrast(a, b){ const l1=lum(a), l2=lum(b);
    const hi=Math.max(l1,l2), lo=Math.min(l1,l2); return hi<=0 ? 1 : (hi+0.05)/(lo+0.05); }
  function effBg(el){ for (let n=el; n && n.nodeType===1; n=n.parentElement) {
      const c = getComputedStyle(n).backgroundColor;
      const x = parse(c); if (x && x.a > 0.9) return c; }
    return 'rgb(0, 0, 0)'; }
  const root = document.querySelector(rootSel);
  if (!root || getComputedStyle(root).display === 'none') return { missing: true };
  // root 自身也在采样列（#mic 这类叶元素：文字直接长在根上，无后代）。
  const out = [];
  for (const e of [root, ...root.querySelectorAll('*')]) {
    const cs = getComputedStyle(e);
    if (cs.display === 'none' || cs.visibility === 'hidden') continue;
    const hasText = [...e.childNodes].some(n => n.nodeType===3 && n.textContent.trim());
    // 图标钮（c_25d2ey）：内容只有 svg.ic，没有文字——按非文本对比度
    // （WCAG 1.4.11，3:1）采样 stroke；currentColor 已由 computed 解析。
    const ic = e.matches('svg.ic') ? e : e.querySelector(':scope > svg.ic');
    if (!hasText && !ic) continue;
    const isIcon = !hasText;
    const fg = isIcon ? getComputedStyle(ic).stroke : cs.color;
    const bg = effBg(e);
    if (!fg || !bg) continue;
    const who = e.tagName.toLowerCase() + (e.id ? '#'+e.id : '')
      + (e.className && typeof e.className==='string' ? '.'+e.className.trim().split(/\\s+/).join('.') : '')
      + (isIcon ? '[icon]' : '');
    const ratio = Math.round(contrast(fg, bg)*100)/100;
    if (!out.some(o => o.fg===fg && o.bg===bg && o.who===who)) out.push({ fg, bg, who, ratio, icon: isIcon });
  }
  return { missing: false, pairs: out };
}`;
const sample = sel => evalJS(`(${SAMPLE_JS})(${JSON.stringify(sel)})`);

// ---- 4. 场景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try {
  console.log(JSON.stringify(await evalJS(`(() => ({
    theme: document.documentElement.getAttribute('data-theme'),
    stored: localStorage.getItem('seek-theme'),
    bodyBg: getComputedStyle(document.body).backgroundColor,
    cls: document.body.className,
  }))()`), null, 1));
} catch { /* 页面已关 */ } };

const until = async (what, jsPred, ms = 8000) => {
  for (let i = 0; i < Math.ceil(ms / 150); i++) {
    await delay(150);
    let v = false;
    try { v = await evalJS(jsPred); } catch { /* 导航中，上下文重建 */ }
    if (v) { trace('就绪：' + what); return true; }
  }
  return false;
};
const waitReady = async () => until('页面就绪',
  `document.readyState==='complete' && !!document.getElementById('set-btn')`);
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);
// 主题三态按钮（面板内真实点击，不直写 localStorage）。
const clickMode = mode => clickSel(`#set-theme button[data-mode="${mode}"]`);
const setCardsReady = `document.querySelectorAll('#set-body .set-card').length>=2`;
// 开设置面板（幂等）：先探「已开且内容齐」——togglePane 对已开面板是关闭，
// 盲点会关掉它；没开才真点 #set-btn。
const openSettings = async () => {
  if (await evalJS(`document.body.classList.contains('settings-view') && ${setCardsReady}`)) return true;
  await clickSel('#set-btn');
  return until('设置面板落定',
    `document.body.classList.contains('settings-view') && ${setCardsReady}`);
};
// 聊天面判定：无面板开（browsing）、无看历史（viewing）。
const isChat = `!document.body.classList.contains('browsing') && !document.body.classList.contains('viewing')`;
// 回聊天面（真实路径）：正文面点「⟵ 返回」，面板面点已开面板的按钮
// （此时按钮文字已是「💬 聊天」，再点即收起）。
const ensureChat = async () => {
  if (await until('已是聊天面', isChat, 400)) return true;
  await evalJS(`(() => {
    if (document.body.classList.contains('viewing')) {
      viewLive(); return;   // 页面上下文：直接调页面函数，别套 Node 侧的 evalJS
    }
    for (const id of ['set-btn','mem-btn','stat-btn','agents-btn']) {
      const b = document.getElementById(id);
      if (b && b.textContent.includes('聊天')) { b.click(); return; }
    }
  })(),'ok'`);
  return until('回聊天面', isChat, 3000);
};

// ============ 景 1：初始态（无记忆位 → 跟随模拟的系统外观）============
trace('景 1：初始态');
if (!await waitReady()) {
  console.error('FAIL: 15s 页面未就绪'); await dump(); process.exit(1);
}
await emulateScheme('light');
// 模拟设置后 matchMedia 变化是事件通知（onSystemTheme→applyTheme），
// 等页面翻稳再读；无记忆位时 bootstrap 与监听器都指向系统值。
await until('初始跟随系统 light', `document.documentElement.getAttribute('data-theme')==='light'`, 3000);
{
  const s = await evalJS(`(() => ({
    stored: localStorage.getItem('seek-theme'),
    theme: document.documentElement.getAttribute('data-theme'),
    bg: getComputedStyle(document.body).backgroundColor,
  }))()`);
  ck('初始无记忆位', s.stored === null, `stored=${s.stored}`);
  ck('初始 data-theme 跟随系统(light)=light', s.theme === 'light', JSON.stringify(s));
  ck('初始亮色底(#ffffff)', s.bg === 'rgb(255, 255, 255)', s.bg);
}

// ============ 景 1b：UI 图标体系（c_25d2ey）============
// 全页 UI 图标 = 内联 <symbol> sprite + <use> 复用；来源/许可证注释在场；
// 可见 UI 文本里无 emoji（消息正文容器是用户/模型内容，排除）。
trace('景 1b：图标体系');
{
  const icon = await evalJS(`(() => {
    const syms = [...document.querySelectorAll('svg.sprite symbol')].map(s => s.id);
    const svgs = [...document.querySelectorAll('svg.ic')];
    const uses = new Set(svgs.map(s => (s.querySelector('use')?.getAttribute('href') || '')));
    const isEmoji = c => { const p = c.codePointAt(0);
      return (p >= 0x1F000 && p <= 0x1FAFF) || (p >= 0x2600 && p <= 0x27BF)
          || (p >= 0x2B00 && p <= 0x2BFF) || p === 0xFE0F; };
    const skip = '.msg, .smsg, .sbody, #mem-content, #ag-last, pre, code, script, style';
    const hits = [];
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    while (w.nextNode()) {
      const n = w.currentNode, el = n.parentElement;
      if (!el || el.closest(skip)) continue;
      if ([...n.nodeValue].some(isEmoji)) hits.push(n.nodeValue.trim().slice(0, 24));
    }
    const first = svgs[0] ? getComputedStyle(svgs[0]) : null;
    const box = svgs[0] ? svgs[0].getBoundingClientRect() : null;
    return { syms: syms.length, icons: svgs.length, uses: uses.size,
      hits: hits.slice(0, 8), stroke: first ? first.stroke : '',
      fill: first ? first.fill : '', w: box ? box.width : 0, h: box ? box.height : 0,
      src: document.documentElement.outerHTML.includes('Lucide')
        && document.documentElement.outerHTML.includes('ISC License') };
  })()`);
  ck('sprite：<symbol> 定义 ≥24 枚', icon.syms >= 24, `syms=${icon.syms}`);
  ck('图标走 <use> 复用（非每处内联 path）', icon.icons >= 8 && icon.uses >= 8, JSON.stringify(icon));
  ck('图标线宽继承：stroke 非 none（currentColor）', !!icon.stroke && icon.stroke !== 'none', icon.stroke);
  ck('图标为线性轮廓：fill=none 且尺寸非零', icon.fill === 'none' && icon.w > 0 && icon.h > 0,
    `fill=${icon.fill} ${icon.w}x${icon.h}`);
  ck('可见 UI 无 emoji（正文容器除外）', icon.hits.length === 0, JSON.stringify(icon.hits));
  ck('sprite 注释带来源与许可证（Lucide / ISC）', icon.src === true, '');
}

// ============ 景 2：防 FOUC——两方向刷新，首帧即目标主题 ============
trace('景 2：防 FOUC');
// light 方向：记忆位=light → reload → 首帧 light + 白底。
if (!await openSettings()) ck('设置面板打开', false, 'set-body 未落定');
clickMode('light');
await until('记忆位=light', `localStorage.getItem('seek-theme')==='light'`);
await evalJS(`window.__firstFrame = undefined, 'ok'`);
reload();
await waitReady();
{
  const s = await evalJS(`(() => ({
    stored: localStorage.getItem('seek-theme'),
    ff: window.__firstFrame || null,
    now: document.documentElement.getAttribute('data-theme'),
  }))()`);
  const ff = s.ff || {};
  ck('记忆位=light', s.stored === 'light', `stored=${s.stored}`);
  ck('light 方向首帧 theme=light（不闪黑）', ff.theme === 'light', JSON.stringify(ff));
  ck('light 方向首帧底色=白（不是暗底）', ff.bg === 'rgb(255, 255, 255)', JSON.stringify(ff));
  ck('light 方向刷新后稳定', s.now === 'light', s.now);
}
// dark 方向：记忆位=dark → reload → 首帧 dark + #151517 底（不闪白）。
// 底色跟着 --seek-inset 走：W6-1 c_pozbmd 把暗底从 #14161a 换成 dsh 实测的 #151517，
// 这条断言是「首帧不是白底」的运行时证据，钉的值只是当前 token 值。
if (!await openSettings()) ck('设置面板重开', false, 'set-body 未落定');
clickMode('dark');
await until('记忆位=dark', `localStorage.getItem('seek-theme')==='dark'`);
await evalJS(`window.__firstFrame = undefined, 'ok'`);
reload();
await waitReady();
{
  const s = await evalJS(`(() => ({
    stored: localStorage.getItem('seek-theme'),
    ff: window.__firstFrame || null,
    now: document.documentElement.getAttribute('data-theme'),
  }))()`);
  const ff = s.ff || {};
  ck('记忆位=dark', s.stored === 'dark', `stored=${s.stored}`);
  ck('dark 方向首帧 theme=dark（不闪白）', ff.theme === 'dark', JSON.stringify(ff));
  ck('dark 方向首帧底色=#151517（不是白底）', ff.bg === 'rgb(21, 21, 23)', JSON.stringify(ff));
  ck('dark 方向刷新后稳定', s.now === 'dark', s.now);
}

// ============ 景 3：设置面板只读口径 + 值钉住 + 关于区 ============
trace('景 3：设置面板');
if (!await openSettings()) ck('设置面板再开', false, 'set-body 未落定');
{
  const rows = await evalJS(`[...document.querySelectorAll('#set-body .set-row')].map(r => ({
    k: r.querySelector('.k').textContent, v: r.querySelector('.v').textContent }))`);
  const find = kw => rows.find(r => r.k.includes(kw)) || { k: kw, v: '(缺行)' };
  const texts = await evalJS(`document.getElementById('set-body').textContent`);
  ck('serve 配置卡行数（外观外的两卡 ≥10 行）', rows.length >= 10, `rows=${rows.length}`);
  ck('默认 provider=deepseek', find('provider').v.includes('deepseek'), find('provider').v);
  ck('默认模型=无配置项口径', find('模型').v.includes('无配置项'), find('模型').v);
  ck('温度=无配置项口径', find('温度').v.includes('无配置项'), find('温度').v);
  ck('并发上限=6（配置值非默认4）', find('并发').v === '6', find('并发').v);
  ck('yolo=开', find('yolo').v.includes('开'), find('yolo').v);
  ck('web 监听非默认端口', /:1879\d/.test(find('监听').v), find('监听').v);
  ck('自动压缩=350000 tokens', find('压缩').v.includes('350000'), find('压缩').v);
  ck('候选项目=1 且含 fixture 路径', find('候选').v === '1' && texts.includes(PROJ),
    find('候选').v);
  const aboutV = find('版本').v;
  ck('版本非空', aboutV.length > 0 && aboutV !== '(缺行)', aboutV);
  ck(`token 短显=…${TOK_TAIL}`, find('token').v === `…${TOK_TAIL}（后 6 位）`, find('token').v);
  ck('改口径文案在场（编辑 config.json 后重启）',
    texts.includes('~/.seek/config.json') && texts.includes('重启'), '');
  // 零写入控件：无 input/select/textarea；button 仅主题三枚。
  const n = await evalJS(`(() => ({
    inp: document.querySelectorAll('#settings input, #settings select, #settings textarea').length,
    themeBtns: [...document.querySelectorAll('#settings button')].filter(b => b.closest('#set-theme')).length,
    otherBtns: [...document.querySelectorAll('#settings button')].filter(b => !b.closest('#set-theme')).length,
  }))()`);
  ck('零写入控件（无 input/select/textarea）', n.inp === 0, JSON.stringify(n));
  ck('按钮仅主题三枚', n.themeBtns === 3 && n.otherBtns === 0, JSON.stringify(n));
}

// ============ 景 4：两主题 × 八面采样，全对 ≥4.5:1 ============
trace('景 4：对比度');
const AA = 4.5;
const iconAA = 3;   // 非文本图形对比度（WCAG 1.4.11）
const checkFace = async (theme, face, sel) => {
  const s = await sample(sel);
  if (s.missing) { ck(`${theme}·${face} 可见`, false, `${sel} 不可见`); return; }
  if (s.pairs.length === 0) { ck(`${theme}·${face} 有可采样元素`, false, `${sel} 空`); return; }
  const need = p => p.icon ? iconAA : AA;
  const bad = s.pairs.filter(p => p.ratio < need(p));
  const min = Math.min(...s.pairs.map(p => p.ratio));
  ck(`${theme}·${face} 对比度达标（文字≥${AA}/图标≥${iconAA}；${s.pairs.length} 对，最低 ${min}）`,
    bad.length === 0, bad.map(p => `${p.who} ${p.fg} on ${p.bg}=${p.ratio}`).join('；'));
};
// 面板开关全走真实按钮：openPane 点未开面板（togglePane 互斥关上一个），
// closePane 点已开面板（按钮此时是「💬 聊天」）。
const openPane = (btn, cls, extra) => clickSel(btn)
  && until(`${btn} 开`, `document.body.classList.contains(${JSON.stringify(cls)})`
    + (extra ? ` && (${extra})` : ''), 6000);
const facesAt = async (theme) => {
  await ensureChat();
  await openPane('#set-btn', 'settings-view', setCardsReady);
  await checkFace(theme, '设置面', '#settings');
  await openPane('#stat-btn', 'stat-view', `document.querySelector('.stat-card')`);
  await checkFace(theme, '统计面', '#stats');
  await openPane('#mem-btn', 'mem-view');
  await checkFace(theme, '记忆面', '#memory');
  await openPane('#agents-btn', 'agents-view');
  await checkFace(theme, '后台(子代理)面', '#agents');
  await ensureChat();
  await checkFace(theme, '会话侧栏', '#side');
  await checkFace(theme, '状态栏', '#status');
  await checkFace(theme, '输入+语音', '#form');
  // 录音态对比度：纯加类采样（不 dispatch 指针事件，不触发真录音），
  // 采完即摘——.rec 的 token 对（--seek-rec/--seek-rec-fg）由此钉住。
  await evalJS(`document.getElementById('mic').classList.add('rec'),'ok'`);
  await checkFace(theme, '语音·录音态', '#mic');
  await evalJS(`document.getElementById('mic').classList.remove('rec'),'ok'`);
  // 会话正文：点 fixture 行（两条消息 + 代码块）。
  clickSel(`#sess-list .sess-row[data-sid="${SID}"]`);
  await until('历史视图开（W6-5）', `document.body.classList.contains('viewing')
    && document.querySelectorAll('#hist .msg').length>=2`);
  await checkFace(theme, '历史视图', '#hist');
  await ensureChat();
};
// 当前记忆位=dark（景 2 留下）——先测 dark 轮，再切 light 测一轮。
await facesAt('dark');
await openPane('#set-btn', 'settings-view', setCardsReady);
clickMode('light');
await until('切到 light', `document.documentElement.getAttribute('data-theme')==='light'`);
await ensureChat();
await facesAt('light');

// ============ 景 5：跟随系统实时翻（清记忆位 + 模拟系统外观变化）============
trace('景 5：跟随系统');
await openPane('#set-btn', 'settings-view', setCardsReady);
clickMode('system');
await until('记忆位已清', `localStorage.getItem('seek-theme')===null`);
{
  const note = await evalJS(`document.getElementById('set-theme-note').textContent`);
  ck('面板说明=跟随系统口径', note.includes('跟随系统') && note.includes('当前生效'), note);
}
await emulateScheme('dark');
await until('系统→dark，页面实时跟随', `document.documentElement.getAttribute('data-theme')==='dark'`);
ck('跟随系统：dark 即时生效', true);
await emulateScheme('light');
await until('系统→light，页面实时跟随', `document.documentElement.getAttribute('data-theme')==='light'`);
ck('跟随系统：light 即时生效', true);
await emulateScheme(null);   // 清模拟，恢复宿主真值

// ---- 收尾 ----
console.log(fails.length === 0 ? '\n无头验收全绿（切换防FOUC/只读口径/两主题八面对比度/系统跟随）'
  : `\nFAIL×${fails.length}：${fails.join('；')}`);
process.exit(fails.length === 0 ? 0 : 1);
