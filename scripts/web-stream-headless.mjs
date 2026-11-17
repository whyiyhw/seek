#!/usr/bin/env node
// web-stream-headless.mjs — W2.1 流式气泡验收的无头浏览器半边
// （scripts/web-stream-manual.sh 的驱动；feature-web-frontend §9 W2.1
// 验收「手测脚本（浏览器/headless）」）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议
// 驱动本机 headless Chrome。消息一律用页面自己的 fetch(q('/msg')) 发出
// ——与真人表单提交同一条线（同 conv、同 cookie、同 Origin 闸路径），
// 脚本本身不持有 token。
//
// 断言三景（桩 seek 按 -p 消息文本切场景，见 web-stream-manual.sh）：
//   /bind   → 指令应答走普通消息路径（run-less reply）
//   流式    → 过程态：streaming 气泡 + ⚙ 工具行 + 部分文本；
//             收口：整泡替换成终文本（含 ``` 围栏渲染的 <pre>），
//             streaming/trow 清零
//   注入    → 模型吐 <script>/<img onerror>：字面渲染为文本、不执行
//             （document.scripts 不增、无 img 落地、标记变量 undefined）
//   卡住+/cancel → 流式气泡被 ack 收口替换 + 「run 已中断。」独立成条
//
// 用法：node scripts/web-stream-headless.mjs <页面URL> <桩项目路径>
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
  console.error('usage: web-stream-headless.mjs <pageUrl> <projDir> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const WATCHDOG_MS = 120_000;
// 总闸 watchdog：任何一景卡死都不许把脚本挂成无限等（CDP 无回包、页面
// 死循环等都可能）——超时退出码 1，现场由各景自身的 dump 兜。
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18792;
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w21-chrome.'));
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
const send = text => evalJS(
  `fetch(q('/msg'), { method: 'POST',
     headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
     body: JSON.stringify({ text: ${JSON.stringify(text)} }) }).then(r => r.status)`);
const snapshot = () => evalJS(`(() => ({
  msgs: [...document.querySelectorAll('#list > .msg')].map(el => ({
    cls: el.className,
    txt: el.textContent,
    trows: [...el.querySelectorAll('.trow')].map(t => t.textContent),
    pres: el.querySelectorAll('pre').length,
  })),
  nstream: document.querySelectorAll('.msg.streaming').length,
  ntrow: document.querySelectorAll('.trow').length,
  nscript: document.scripts.length,
  nimg: document.querySelectorAll('img').length,
  xss1: typeof window.__xss, xss2: typeof window.__xss2,
}))()`);

// ---- 3. 场景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snapshot(), null, 1)); } catch { /* 页面已关 */ } };

// 就绪：/state 探测落定（不再「连接中」即 SSE 判定过鉴权并已挂流）。
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
const baseScript = await snapshot();   // 基线：脚本数（页面自身 1 个）
ck('基线 scripts=1（仅页面内联脚本）', baseScript.nscript === 1, `got ${baseScript.nscript}`);

// 轮询直到谓词过或超时；返回最后一次快照。history 收集过程态。
async function until(what, pred, ms) {
  const t0 = Date.now();
  let s = await snapshot();
  const history = [s];
  while (!pred(s)) {
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(120);
    s = await snapshot();
    history.push(s);
  }
  return { s, history };
}

// 景 0：/bind —— 指令应答（run-less reply）走普通消息路径。
trace('景 0：/bind');
if ((await send(`/bind ${projDir}`)) !== 202) { console.error('FAIL: /bind 非 202'); await dump(); process.exit(1); }
try {
  const { s } = await until('bind 应答', s => s.msgs.some(m => m.txt.includes('已绑定项目')), 8000);
  ck('/bind 应答成条（run-less 普通消息）', s.msgs.some(m => m.txt.includes('已绑定项目')), '');
} catch (e) { ck('/bind 应答成条', false, e.message); await dump(); }

// 景 1：流式 —— 过程态（气泡+工具行+部分文本）与收口（整泡替换）。
trace('景 1：流式');
try {
  if ((await send('流式')) !== 202) throw new Error('POST 非 202');
  const { s, history } = await until('流式收口',
    s => s.nstream === 0 && s.msgs.some(m => m.txt.includes('完结。')), 15000);
  const sawLive = history.some(h => h.nstream > 0 && h.ntrow > 0
    && h.msgs.some(m => m.cls.includes('streaming') && m.txt.startsWith('Hel')));
  ck('过程态：streaming 气泡 + ⚙ 工具行 + 部分文本可见', sawLive,
    `history ${history.length} 帧中无同帧匹配（nstream/ntrow 轨迹：`
    + history.map(h => h.nstream + '/' + h.ntrow).join(' ') + '）');
  ck('工具行折叠出现过 read（start→end 同行幂等）',
    history.some(h => h.msgs.some(m => m.trows.some(t => t.includes('read')))), '');
  const fin = s.msgs.find(m => m.txt.includes('完结。'));
  ck('收口：终文本整泡替换（三段俱全、工具行不留残）',
    !!fin && fin.txt.includes('Hello 世界。') && fin.txt.includes('func ok() {}')
    && !fin.txt.includes('⚙') && !fin.trows.length,
    JSON.stringify(s.msgs.map(m => m.txt)));
  ck('收口：围栏渲染成 <pre>（markdown 路径未被流式破坏）', !!fin && fin.pres > 0, '');
  ck('收口：streaming/trow 清零', s.nstream === 0 && s.ntrow === 0,
    `nstream=${s.nstream} ntrow=${s.ntrow}`);
} catch (e) { ck('流式场景', false, e.message); await dump(); }

// 景 2：注入 —— 模型吐 <script>/<img onerror>，字面渲染、不执行。
trace('景 2：注入');
try {
  if ((await send('注入')) !== 202) throw new Error('POST 非 202');
  const { s } = await until('注入收口', s => s.nstream === 0 && s.msgs.some(m => m.txt.includes('纯文本')), 15000);
  const last = s.msgs[s.msgs.length - 1];
  ck('XSS：<script> 字面渲染为文本', last && last.txt.includes('<script>window.__xss=1</script>'),
    JSON.stringify(last && last.txt));
  ck('XSS：<img onerror> 字面渲染为文本', last && last.txt.includes('<img src=x onerror="window.__xss2=1">'), '');
  ck('XSS：脚本未执行（window.__xss undefined）', s.xss1 === 'undefined', `got ${s.xss1}`);
  ck('XSS：onerror 未执行（window.__xss2 undefined）', s.xss2 === 'undefined', `got ${s.xss2}`);
  ck('XSS：document.scripts 不增（仍 ' + baseScript.nscript + '）', s.nscript === baseScript.nscript, `got ${s.nscript}`);
  ck('XSS：无 img 元素落地', s.nimg === 0, `got ${s.nimg}`);
} catch (e) { ck('注入场景', false, e.message); await dump(); }

// 景 3：卡住 + /cancel —— 气泡被 ack 收口，终态回复独立成条（run 幂等）。
trace('景 3：卡住+/cancel');
try {
  if ((await send('卡住')) !== 202) throw new Error('POST 非 202');
  await until('流式气泡出现', s => s.nstream > 0, 8000);
  if ((await send('/cancel')) !== 202) throw new Error('POST /cancel 非 202');
  const { s } = await until('中断终态', s => s.nstream === 0
    && s.msgs.some(m => m.txt.includes('已中断 当前 run'))
    && s.msgs.some(m => m.txt.includes('run 已中断')), 10000);
  ck('cancel：ack 收口气泡（已中断 当前 run）',
    s.msgs.some(m => m.txt.includes('已中断 当前 run')), '');
  ck('cancel：终态「run 已中断。」独立成条', s.msgs.some(m => m.txt.includes('run 已中断')), '');
  ck('cancel：无残留 streaming 气泡', s.nstream === 0, `got ${s.nstream}`);
} catch (e) { ck('cancel 场景', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（W2.1 无头验收）');
process.exit(fails.length ? 1 : 0);
