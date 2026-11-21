#!/usr/bin/env node
// web-md-headless.mjs — W5 markdown 渲染（卡 c_x2au4t）的无头浏览器验收
// 半边（scripts/web-md-manual.sh 的驱动）。三景对应卡面验收 ①②：
//   渲染   → 收口后的权威气泡里：h2/h3 标题、ul/ol 列表、行内 <code>、
//            <pre> 围栏、协议白名单内的 <a>（http/https/相对解析）、
//            javascript: 链整链字面（不成为可点 <a>）
//   注入   → 模型吐 <script>/<div onclick>/<a href=javascript:>/[x](javascript:)
//            全部字面渲染为文本：属性选择器 [onclick]/[onerror] 零命中、
//            javascript: href 零命中、window.__xss* undefined、
//            document.scripts 恒 3（head 主题 bootstrap + 页面内联 + static/markdown.js）
//   流式   → 过程不闪烁：气泡同一元素贯穿（标记法）、文本单调不缩（构造
//            只追加的 delta 序列）、streaming 态可见；收口整泡重渲成
//            权威 md 形状（标题/列表/围栏俱全）
//
// 零依赖：Node ≥22 + 本机 headless Chrome（CDP）。消息走页面自己的
// fetch(q('/msg'))——与真人同线，脚本不持有 token。
//
// 用法：node scripts/web-md-headless.mjs <页面URL> <桩项目路径>
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
const projDir = args[1];
if (!pageUrl || !projDir) {
  console.error('usage: web-md-headless.mjs <pageUrl> <projDir> [--chrome path]');
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
const WATCHDOG_MS = 120_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18794;   // W2.1 用 18792、W4.4 用 18793；错开免互抢
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5md-chrome.'));
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
const send = text => evalJS(
  `fetch(q('/msg'), { method: 'POST',
     headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
     body: JSON.stringify({ text: ${JSON.stringify(text)} }) }).then(r => r.status)`);
// 气泡快照：正文形状（标题/列表/链接/围栏/行内码）+ 注入面（属性选择器/
// 脚本数/img）+ 流式标记（同元素贯穿的 __mdmark 由本脚本在首见时打下）。
const snapshot = () => evalJS(`(() => {
  const bubble = document.querySelector('#list > .msg:last-child');
  return {
    n: document.querySelectorAll('#list > .msg').length,
    cls: bubble ? bubble.className : '',
    txt: bubble ? bubble.textContent : '',
    heads: bubble ? [...bubble.querySelectorAll('h1,h2,h3,h4,h5,h6')].map(h => h.tagName + ':' + h.textContent) : [],
    ul: bubble ? bubble.querySelectorAll('ul').length : 0,
    ol: bubble ? bubble.querySelectorAll('ol').length : 0,
    lis: bubble ? [...bubble.querySelectorAll('li')].map(l => l.textContent) : [],
    pres: bubble ? [...bubble.querySelectorAll('pre')].map(p => p.textContent) : [],
    codes: bubble ? [...bubble.querySelectorAll('code')].map(c => c.textContent) : [],
    links: bubble ? [...bubble.querySelectorAll('a')].map(a => ({ href: a.href, rel: a.rel, target: a.target, text: a.textContent })) : [],
    nstream: document.querySelectorAll('.msg.streaming').length,
    mark: bubble ? (bubble.__mdmark || 0) : -1,
    nscript: document.scripts.length,
    nimg: document.querySelectorAll('img').length,
    evHandlers: document.querySelectorAll('[onclick],[onerror],[onload]').length,
    jsLinks: document.querySelectorAll('a[href^="javascript:"]').length,
    xss1: typeof window.__xss, xss2: typeof window.__xss2, xss3: typeof window.__xss3,
  };
})()`);
const markBubble = () => evalJS(`(() => {
  const b = document.querySelector('#list > .msg:last-child');
  if (b && !b.__mdmark) b.__mdmark = 1;
  return b ? (b.__mdmark || 0) : -1;
})()`);

// ---- 3. 场景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snapshot(), null, 1)); } catch { /* 页面已关 */ } };

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
ck('基线 scripts=3（head 主题 bootstrap + 内联页面脚本 + static/markdown.js）', base.nscript === BASE_SCRIPTS, `got ${base.nscript}`);
ck('渲染器已载（window.seekMD）', await evalJS('typeof window.seekMD') === 'object',
  await evalJS('typeof window.seekMD'));

// 轮询直到谓词过或超时；history 收集过程态（闪烁断言的样本源）。
async function until(what, pred, ms) {
  const t0 = Date.now();
  let s = await snapshot();
  const history = [s];
  while (!pred(s)) {
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(100);
    s = await snapshot();
    history.push(s);
  }
  return { s, history };
}

// 景 0：/bind —— 桩项目绑定（后续场景的前提）。
trace('景 0：/bind');
if ((await send(`/bind ${projDir}`)) !== 202) { console.error('FAIL: /bind 非 202'); await dump(); process.exit(1); }
try {
  const { s } = await until('bind 应答', s => s.txt.includes('已绑定项目'), 8000);
  ck('/bind 应答成条', s.txt.includes('已绑定项目'), '');
} catch (e) { ck('/bind 应答成条', false, e.message); await dump(); }

// 景 1：渲染 —— 收口后的权威气泡是 md 形状（验收 ① 的「含 md 消息渲染
// 正确」半边；样例与 web-md-manual.sh 桩同源）。
trace('景 1：渲染');
try {
  if ((await send('渲染')) !== 202) throw new Error('POST 非 202');
  const { s } = await until('渲染收口', s => s.nstream === 0 && s.txt.includes('收束行'), 15000);
  ck('标题：h2/h3 各就位', s.heads.includes('H2:标题二') && s.heads.includes('H3:标题三'),
    JSON.stringify(s.heads));
  ck('列表：ul/ol 各一、条目齐', s.ul === 1 && s.ol === 1
    && s.lis.includes('无序甲') && s.lis.includes('无序乙')
    && s.lis.includes('有序一') && s.lis.includes('有序二'), JSON.stringify(s.lis));
  ck('行内码：`code` → <code>', s.codes.includes('code'), JSON.stringify(s.codes));
  ck('围栏：```go → <pre> 且语言行不进正文', s.pres.includes('func md() {}'), JSON.stringify(s.pres));
  const https = s.links.find(l => l.href === 'https://example.com/x?a=1');
  ck('链接：https 直取 + 新开页 + noopener',
    !!https && https.target === '_blank' && /noopener/.test(https.rel) && https.text === '外链',
    JSON.stringify(s.links));
  ck('链接：相对 /state 解析为同源绝对', s.links.some(l => l.href.endsWith('/state') && l.text === '相对'),
    JSON.stringify(s.links));
  ck('链接：javascript: 整链字面（不成为 <a>）',
    s.txt.includes('[坏链](javascript:alert(1))') && !s.links.some(l => l.href.startsWith('javascript:')),
    JSON.stringify(s.links));
} catch (e) { ck('渲染场景', false, e.message); await dump(); }

// 景 2：注入 —— 恶意 script/onclick/javascript: 全字面（验收 ① 的「恶意
// script/onclick 被转义」半边）。
trace('景 2：注入');
try {
  if ((await send('注入')) !== 202) throw new Error('POST 非 202');
  const { s } = await until('注入收口', s => s.nstream === 0 && s.txt.includes('上面应全部为纯文本'), 15000);
  ck('XSS：<script> 字面渲染为文本', s.txt.includes('<script>window.__xss=1</script>'), '');
  ck('XSS：<div onclick> 字面渲染为文本', s.txt.includes('<div onclick="window.__xss2=1">div</div>'), '');
  ck('XSS：<a href=javascript:> 字面渲染为文本', s.txt.includes('<a href="javascript:window.__xss3=1">点我</a>'), '');
  ck('XSS：[x](javascript:) 字面渲染为文本', s.txt.includes('[点我](javascript:void(0))'), '');
  ck('XSS：零 on* 属性元素', s.evHandlers === 0, `got ${s.evHandlers}`);
  ck('XSS：零 javascript: href', s.jsLinks === 0 && !s.links.some(l => l.href.startsWith('javascript:')), `got ${s.jsLinks}`);
  ck('XSS：脚本未执行（__xss/2/3 全 undefined）',
    s.xss1 === 'undefined' && s.xss2 === 'undefined' && s.xss3 === 'undefined',
    `${s.xss1}/${s.xss2}/${s.xss3}`);
  ck('XSS：document.scripts 不增（仍 ' + BASE_SCRIPTS + '）', s.nscript === BASE_SCRIPTS, `got ${s.nscript}`);
  ck('XSS：无 img 元素落地', s.nimg === 0, `got ${s.nimg}`);
} catch (e) { ck('注入场景', false, e.message); await dump(); }

// 景 3：流式 —— 过程不闪烁（验收 ②）：delta 序列只追加（无中途闭合的
// 构造），气泡同一元素贯穿、文本单调不缩；收口整泡重渲成权威 md 形状。
trace('景 3：流式');
try {
  if ((await send('流式')) !== 202) throw new Error('POST 非 202');
  // 首见 streaming 气泡即打标记：此后每次快照 mark===1 即同一元素（气泡
  // 没有被拆掉重建=无整泡闪烁）。
  await until('流式气泡出现', s => s.nstream > 0, 8000);
  ck('标记落在 streaming 气泡上', (await markBubble()) === 1, '');
  const { s, history } = await until('流式收口',
    s => s.nstream === 0 && s.txt.includes('流式完结。'), 15000);
  const live = history.filter(h => h.nstream > 0 && h.txt.length > 0 && h.mark !== -1);
  ck('过程态采样 ≥3 帧（流式期间确实在渲）', live.length >= 3, `got ${live.length}`);
  ck('气泡同一元素贯穿（标记不丢 = 无拆泡重建）', live.every(h => h.mark === 1),
    `marks: ${live.map(h => h.mark).join('')}`);
  const lens = live.map(h => h.txt.length);
  const shrinks = lens.slice(1).filter((n, i) => n < lens[i]).length;
  ck('文本单调不缩（追加式 delta 无闪烁回退）', shrinks === 0,
    `lengths: ${lens.join('>')}`);
  ck('收口：整泡重渲成 md 形状（标题+列表+围栏）',
    s.nstream === 0 && s.heads.some(h => h.startsWith('H2:'))
    && s.lis.includes('流式条目') && s.pres.includes('render(md)'),
    JSON.stringify({ heads: s.heads, lis: s.lis, pres: s.pres }));
  ck('收口：权威文本完整', s.txt.includes('流式正文第一段。') && s.txt.includes('流式完结。'), '');
} catch (e) { ck('流式场景', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（W5 markdown 无头验收）');
process.exit(fails.length ? 1 : 0);
