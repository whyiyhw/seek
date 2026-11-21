#!/usr/bin/env node
// web-stop-headless.mjs — c_5m3fm6 生成中停止键验收的无头浏览器半边
// （scripts/web-stop-manual.sh 的驱动；对齐 dsh InputBar 中断语义）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议
// 驱动本机 headless Chrome。消息一律用页面自己的 fetch(q('/msg')) 发出
// ——与真人表单提交同一条线（同 conv、同 cookie、同 Origin 闸路径），
// 脚本本身不持有 token；停止则用元素 .click() 走真实的点击分岔（click
// 层 preventDefault → sendText('/cancel')），不是替页面调函数。
//
// 断言三景（桩 seek 按 -p 消息文本切场景，见 web-stop-manual.sh）：
//   /bind   → 空态：发送键恒「发送」、无 stop 态（③前半）
//   慢速    → 生成中：键变「⏹ 停止」红底（①）；输入框留草稿后点停止：
//             草稿不动、点击以 /cancel 指令透传（you 气泡 '/cancel'）、
//             半截气泡保留 + 尾部「—— 已中断 ——」、无 ack 整泡替换、
//             「run 已中断。」独立成条、streaming 清零（②）
//   继续    → 中断后发新消息：正常流式收口（②后半）；键回「发送」、
//             无 stop 态（③后半）
//
// 用法：node scripts/web-stop-headless.mjs <页面URL> <桩项目路径>
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
  console.error('usage: web-stop-headless.mjs <pageUrl> <projDir> [--chrome path]');
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
const dbgPort = 18799;
const profile = mkdtempSync(path.join(tmpdir(), 'seek-stop-chrome.'));
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
const clickStop = () => evalJS(`document.getElementById('send').click(), 'clicked'`);
const snapshot = () => evalJS(`(() => ({
  msgs: [...document.querySelectorAll('#list > .msg')].map(el => ({
    cls: el.className,
    txt: el.textContent,
  })),
  nstream: document.querySelectorAll('.msg.streaming').length,
  sendLabel: document.getElementById('send').textContent,
  sendStop: document.getElementById('send').classList.contains('stop'),
  sendDisabled: document.getElementById('send').disabled,
  draft: document.getElementById('input').value,
}))()`);

// ---- 3. 场景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : 'FAIL') + ' ' + name + (ok ? '' : '：' + detail));
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

// 景 0：/bind + 空态键形（③前半：无生成恒发送键）。
trace('景 0：/bind + 空态');
{
  const s0 = await snapshot();
  ck('空态：发送键=「发送」（无 stop 态，③）', s0.sendLabel === '发送' && !s0.sendStop,
    `label=${s0.sendLabel} stop=${s0.sendStop}`);
  if ((await send(`/bind ${projDir}`)) !== 202) { console.error('FAIL: /bind 非 202'); await dump(); process.exit(1); }
  try {
    const { s } = await until('bind 应答', s => s.msgs.some(m => m.txt.includes('已绑定项目')), 8000);
    ck('/bind 应答成条', s.msgs.some(m => m.txt.includes('已绑定项目')), '');
    ck('bind 后仍无 stop 态（③）', !s.sendStop, `label=${s.sendLabel}`);
  } catch (e) { ck('/bind 应答成条', false, e.message); await dump(); }
}

// 景 1：慢速 → 生成中键态（①）→ 草稿 + 点停止 → 保留式中断收口（②）。
trace('景 1：慢速+点停止');
try {
  if ((await send('慢速')) !== 202) throw new Error('POST 非 202');
  const { s: live } = await until('停止键出现', s => s.sendStop && s.nstream > 0, 8000);
  ck('①生成中：发送键变「⏹ 停止」', live.sendLabel === '⏹ 停止' && live.sendStop,
    `label=${live.sendLabel} stop=${live.sendStop}`);
  ck('①生成中：半截已上屏（流确实在跑）',
    live.msgs.some(m => m.cls.includes('streaming') && m.txt.includes('半截')), '');

  // 草稿在场点停止：草稿必须原样（fromInput=false），点击只发 /cancel。
  await evalJS(`document.getElementById('input').value = '草稿别动'`);
  await clickStop();
  const { s } = await until('中断终态', s => s.nstream === 0
    && s.msgs.some(m => m.txt.includes('—— 已中断 ——'))
    && s.msgs.some(m => m.txt.includes('run 已中断')), 10000);
  ck('②点击停止=透传 /cancel（you 气泡为指令文本）',
    s.msgs.some(m => m.cls.includes('you') && m.txt.trim() === '/cancel'),
    JSON.stringify(s.msgs.map(m => m.txt)));
  ck('②草稿原样保留（停止不吃输入框）', s.draft === '草稿别动', `got ${JSON.stringify(s.draft)}`);
  const cut = s.msgs.find(m => m.txt.includes('—— 已中断 ——'));
  ck('②半截气泡保留（含已上屏文本）+ 尾部标注',
    !!cut && cut.txt.includes('半截'), cut && cut.txt);
  ck('②ack 不再整泡替换（气泡内无「已中断 当前 run」）',
    !s.msgs.some(m => m.txt.includes('已中断 当前 run')), '');
  ck('②终态「run 已中断。」独立成条', s.msgs.some(m => m.txt.includes('run 已中断')), '');
  ck('②无残留 streaming 气泡', s.nstream === 0, `got ${s.nstream}`);
} catch (e) { ck('停止场景', false, e.message); await dump(); }

// 景 2：中断后继续发新消息（②后半）+ 键复位（③后半）。走真实表单提交
// 路径（submit 事件 → sendText 守卫 → POST），不是裸 fetch——验收的是
// 「输入栏可继续用」，含草稿清空（fromInput=true）。
trace('景 2：中断后再发');
try {
  await evalJS(`(() => {
    document.getElementById('input').value = '继续';
    document.getElementById('form').dispatchEvent(new Event('submit', { cancelable: true }));
    return 'submitted';
  })()`);
  const { s } = await until('新消息收口', s => s.nstream === 0
    && s.msgs.some(m => m.txt.includes('桩应答') && m.txt.includes('继续')), 10000);
  ck('②中断后可继续发新消息（真实 submit 路径，新 run 流式收口）',
    s.msgs.some(m => m.txt.includes('桩应答') && m.txt.includes('继续')),
    JSON.stringify(s.msgs.map(m => m.txt)));
  ck('②表单路径完整：草稿已清空（fromInput）', s.draft === '', `got ${JSON.stringify(s.draft)}`);
  ck('③收口后发送键回「发送」（无 stop 态）', s.sendLabel === '发送' && !s.sendStop,
    `label=${s.sendLabel} stop=${s.sendStop}`);
} catch (e) { ck('中断后再发场景', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（c_5m3fm6 停止键无头验收）');
process.exit(fails.length ? 1 : 0);
