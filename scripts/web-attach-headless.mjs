#!/usr/bin/env node
// web-attach-headless.mjs — W5 附件上传（卡 c_56yrcj）的无头浏览器验收
// 半边（scripts/web-attach-manual.sh 的驱动）。三入口各一景，全部走页面
// 自己的真实监听器（脚本只构造事件，不调用页面内部函数）：
//
//   点击选择 → 给 #file 塞 File 并派发 change
//   拖放     → 派发 dragenter/dragover/drop（带 DataTransfer）
//   粘贴     → 派发 paste（带 ClipboardEvent 的 clipboardData）
//
// 断言的不只是「托盘出现」：每个入口上传成功后，页面把服务端返回的绝对
// 路径拼成 "@<path>" 追加进 /msg 正文——桩 seek 把它写进 argv 日志，脚本
// 读那条日志，证明「附件路径真的走到了 CLI」，而那正是 modelcap 视觉车道
// 与 Files API 文档车道的既有入口。
//
// 失败面：不支持的类型（本地预检）与改名的坏文件（服务端 415）都必须
// 在托盘里出可读文案，且不进正文。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 CDP 驱动本机 headless
// Chrome。脚本不持有 token（页面自己带 cookie）。
//
// 用法：node scripts/web-attach-headless.mjs <页面URL> <桩项目路径> <argv日志>
//       [--chrome <chrome 可执行文件>]
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync, readFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const chromeIdx = argv.indexOf('--chrome');
const CHROME = chromeIdx > 0 ? argv[chromeIdx + 1]
  : process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const args = argv.filter((a, i) => a !== '--chrome' && i !== chromeIdx + 1);
const [pageUrl, projDir, argvLog] = args;
if (!pageUrl || !projDir || !argvLog) {
  console.error('usage: web-attach-headless.mjs <pageUrl> <projDir> <argvLog> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const WATCHDOG_MS = 150_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome ----
const dbgPort = 18794;
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5att-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${dbgPort}`,
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
    throw new Error('page eval failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 400));
  }
  return r.result.value;
}

// ---- 3. 页面侧的小工具 ----
// PNG 的 base64（16×16 单色）——真实可解码的图片，CSP 的 img-src data: 是它
// 能在气泡里渲染的前提。
const PNG_B64 = 'iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAYAAAAf8/9hAAAAJUlEQVR42mP8z8B'
  + 'Qz0AEYBxVSF+FjIyMDIz///9nGFVIX4UAt3kH8PZm2sYAAAAASUVORK5CYII=';

const snapshot = () => evalJS(`(() => ({
  chips: [...document.querySelectorAll('#tray .chip')].map(c => ({
    name: c.querySelector('.nm') ? c.querySelector('.nm').textContent : '',
    pct: c.querySelector('.pc') ? c.querySelector('.pc').textContent : '',
    bad: c.classList.contains('bad'),
  })),
  trayOpen: document.getElementById('tray').classList.contains('has'),
  msgs: [...document.querySelectorAll('#list > .msg')].map(el => ({
    cls: el.className,
    txt: el.textContent,
    thumbs: el.querySelectorAll('.thumbs img').length,
    docs: el.querySelectorAll('.thumbs .doc').length,
  })),
  hints: [...document.querySelectorAll('#list > .msg.hint')].map(e => e.textContent),
}))()`);

async function until(what, pred, ms) {
  const t0 = Date.now();
  let s = await snapshot();
  while (!pred(s)) {
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(120);
    s = await snapshot();
  }
  return s;
}

const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snapshot(), null, 1)); } catch { /* page gone */ } };

// sendViaPage 走页面自己的表单路径（同 sentinel：填 #input + 派发 submit），
// 与真人回车完全同一条线。
const sendViaPage = text => evalJS(`(() => {
  const el = document.getElementById('input');
  el.value = ${JSON.stringify(text)};
  document.getElementById('form').dispatchEvent(new Event('submit', { cancelable: true }));
  return 'ok';
})()`);

// filePayload 在页面里造一个 File 对象，返回它的元信息（不返回 File 本身）。
const makePngFile = name => evalJS(`(() => {
  const bin = atob(${JSON.stringify(PNG_B64)});
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  window.__probe = new File([buf], ${JSON.stringify(name)}, { type: 'image/png' });
  return window.__probe.size;
})()`);

const clickEntry = name => evalJS(`(() => {
  const dt = new DataTransfer();
  dt.items.add(new File([window.__probe], ${JSON.stringify(name)}, { type: 'image/png' }));
  const input = document.getElementById('file');
  input.files = dt.files;
  const n = input.files.length;   // 处理器会清空 input.value（连带 FileList），先读
  input.dispatchEvent(new Event('change', { bubbles: true }));
  return n;
})()`);

const dropEntry = name => evalJS(`(() => {
  const dt = new DataTransfer();
  dt.items.add(new File([window.__probe], ${JSON.stringify(name)}, { type: 'image/png' }));
  for (const type of ['dragenter', 'dragover']) {
    document.dispatchEvent(new DragEvent(type, { bubbles: true, dataTransfer: dt }));
  }
  document.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt }));
  return dt.files.length;
})()`);

const pasteEntry = name => evalJS(`(() => {
  const dt = new DataTransfer();
  dt.items.add(new File([window.__probe], ${JSON.stringify(name)}, { type: 'image/png' }));
  const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: dt });
  document.dispatchEvent(ev);
  return ev.clipboardData && ev.clipboardData.files ? ev.clipboardData.files.length : 0;
})()`);

// 服务端 415 / 本地预检失败的入口：抛一个「名字说是 PNG、内容其实是脚本」
// 的文件（与坏文件同形）。
const badEntry = (name, content, mime) => evalJS(`(() => {
  const dt = new DataTransfer();
  dt.items.add(new File([${JSON.stringify(content)}], ${JSON.stringify(name)},
    { type: ${JSON.stringify(mime)} }));
  const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: dt });
  document.dispatchEvent(ev);
  return 'ok';
})()`);

const readArgvLog = () => {
  if (!existsSync(argvLog)) return '';
  return readFileSync(argvLog, 'utf8');
};

// ---- 4. 就绪 ----
trace('等页面就绪');
for (let i = 0; i < 120; i++) {
  await delay(150);
  let ready = '';
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
  } catch { /* 页面尚未执行到脚本 */ }
  if (ready) break;
  if (i === 119) { console.error('FAIL: 18s 页面未就绪'); process.exit(1); }
}

// /bind：后文的消息要有一个绑定的项目，桩 seek 才拿得到 -p。
trace('景 0：/bind');
await sendViaPage(`/bind ${projDir}`);
try {
  await until('bind 应答', s => s.msgs.some(m => m.txt.includes('已绑定项目')), 10000);
} catch (e) { ck('/bind 就绪', false, e.message); await dump(); process.exit(1); }

// ---- 景 1：点击选择 ----
trace('景 1：点击选择');
const clickName = 'click shot 截图.png';
try {
  if ((await makePngFile(clickName)) < 60) throw new Error('fixture 太小');
  const n = await clickEntry(clickName);
  ck('点击入口：change 事件带 1 个 file', n === 1, `got ${n}`);
  const s = await until('托盘就绪', st => st.chips.length === 1 && st.chips[0].pct === '已就绪', 10000);
  ck('点击入口：托盘出现且进度收口为「已就绪」', s.chips.length === 1 && s.chips[0].pct === '已就绪',
    JSON.stringify(s.chips));
  ck('点击入口：非 ASCII/空格文件名原样显示（服务端另存为安全名）',
    s.chips[0].name.includes('截图'), s.chips[0].name);

  const before = readArgvLog().split('\n').length;
  await sendViaPage('看图');
  const s2 = await until('消息气泡缩略图',
    st => st.msgs.some(m => m.cls.includes('you') && m.thumbs >= 1), 15000);
  const bubble = s2.msgs.find(m => m.cls.includes('you') && m.thumbs >= 1);
  ck('点击入口：用户气泡带图片缩略图', !!bubble, JSON.stringify(s2.msgs));
  ck('点击入口：气泡正文仍是用户原文（@路径不进气泡）',
    !!bubble && bubble.txt.startsWith('看图') && !bubble.txt.includes('@/'), bubble && bubble.txt);

  // 真的走到了 CLI：桩 seek 的 argv 里有 @<绝对路径>
  let log = '';
  for (let i = 0; i < 50; i++) {
    log = readArgvLog();
    if (log.split('\n').filter(Boolean).length > before && /@\S+\.png/.test(log)) break;
    await delay(120);
  }
  const line = log.split('\n').filter(l => l.includes('@')).pop() || '';
  ck('点击入口：@路径进了子进程 argv（vision 车道的入口）', /@\S+\.png/.test(line), line || '(argv 日志里没有 @ 路径)');
  ck('点击入口：发完后托盘空了', (await snapshot()).chips.length === 0, 'tray 未清空');
} catch (e) { ck('点击入口', false, e.message); await dump(); }

// ---- 景 2：拖放 ----
trace('景 2：拖放');
try {
  await makePngFile('dropped.png');
  const n = await dropEntry('dropped.png');
  ck('拖放入口：drop 事件带 1 个 file', n === 1, `got ${n}`);
  const s = await until('拖放托盘就绪', st => st.chips.some(c => c.name === 'dropped.png' && c.pct === '已就绪'), 10000);
  ck('拖放入口：托盘就绪', s.chips.some(c => c.name === 'dropped.png' && c.pct === '已就绪'),
    JSON.stringify(s.chips));
  ck('拖放入口：落点提示类已移除', await evalJS(`!document.body.classList.contains('dropping')`), 'dropping 未清');
} catch (e) { ck('拖放入口', false, e.message); await dump(); }

// ---- 景 3：粘贴 ----
trace('景 3：粘贴');
try {
  await makePngFile('pasted.png');
  const n = await pasteEntry('pasted.png');
  ck('粘贴入口：clipboardData.files 带 1 个 file', n === 1, `got ${n}`);
  const s = await until('粘贴托盘就绪', st => st.chips.some(c => c.name === 'pasted.png' && c.pct === '已就绪'), 10000);
  ck('粘贴入口：托盘就绪', s.chips.some(c => c.name === 'pasted.png' && c.pct === '已就绪'),
    JSON.stringify(s.chips));
} catch (e) { ck('粘贴入口', false, e.message); await dump(); }

// ---- 景 4：失败提示（超限/坏文件）----
trace('景 4：失败提示');
try {
  // 本地预检：不支持的类型
  await badEntry('notes.txt', 'plain text', 'text/plain');
  let s = await until('不支持类型提示', st => st.chips.some(c => c.name === 'notes.txt' && c.bad), 5000);
  const chip = s.chips.find(c => c.name === 'notes.txt');
  ck('坏文件：不支持的类型在托盘里标红并给文案',
    !!chip && chip.bad && chip.pct.includes('不支持'), JSON.stringify(s.chips));

  // 服务端魔数：改名成 .png 的脚本 → 415 → 托盘显示服务端文案
  await badEntry('renamed.png', '#!/bin/sh\necho nope\n', 'image/png');
  s = await until('服务端 415 提示',
    st => st.chips.some(c => c.name === 'renamed.png' && c.bad && c.pct.includes('扩展名')), 8000);
  const chip2 = s.chips.find(c => c.name === 'renamed.png');
  ck('坏文件：改名的非图片被服务端拒（魔数不符文案）',
    !!chip2 && chip2.bad && chip2.pct.includes('扩展名'), JSON.stringify(s.chips));

  // 失败项不许进正文：发一条消息，气泡里不能有 @路径；页面应提示未就绪
  await sendViaPage('');
  s = await until('未就绪提示',
    st => st.hints.some(h => h.includes('未就绪')), 5000);
  ck('坏文件：发送前的失败项提示可见', s.hints.some(h => h.includes('未就绪')), JSON.stringify(s.hints));

  // 清掉失败项后托盘收起
  await evalJS(`[...document.querySelectorAll('#tray .chip .rm')].forEach(b => b.click()); 'ok'`);
  s = await snapshot();
  ck('失败项可移除，托盘随之收起', s.chips.length === 0 && !s.trayOpen, JSON.stringify(s));
} catch (e) { ck('失败提示', false, e.message); await dump(); }

// ---- 景 5：零回归（CSP/页面结构/文本命令）----
trace('景 5：零回归');
try {
  const csp = await evalJS(`fetch('/').then(r => r.headers.get('Content-Security-Policy'))`);
  ck("CSP 仍是 'self' 基线 + img-src data:", /^default-src 'self'/.test(csp) && csp.includes("img-src 'self' data:"), csp);
  ck('页面无 HTML 解析 sink', !(await evalJS(`/innerHTML|insertAdjacentHTML|document\\.write/.test(document.documentElement.outerHTML)`)), '');
  const scripts = await evalJS('document.scripts.length');
  // 3 = head 主题 bootstrap（c_ni7mzw）+ 页面内联脚本 + static/markdown.js。
  ck('脚本数仍是 3（head 主题 bootstrap + 内联 + static/markdown.js）', scripts === 3, `got ${scripts}`);
  await sendViaPage('/projects');
  const s = await until('文本指令仍可用', st => st.msgs.some(m => m.txt.includes('候选项目')), 8000);
  ck('纯文本指令零回归（/projects 应答）', s.msgs.some(m => m.txt.includes('候选项目')), '');
} catch (e) { ck('零回归', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（W5 附件上传无头验收）');
process.exit(fails.length ? 1 : 0);
