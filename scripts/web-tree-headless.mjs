#!/usr/bin/env node
// web-tree-headless.mjs — c_ux7lvk 工具调用树验收的无头浏览器半边
// （scripts/web-tree-manual.sh 的驱动）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议
// 驱动本机 headless Chrome。消息一律用页面自己的 fetch(q('/msg')) 发出。
//
// 断言三面（桩 seek 按 -p 消息文本切场景，见 web-tree-manual.sh）：
//   流式面 → 「树」场景：
//     过程态：三行调用树——grep 顶层、read 缩进一层（区间包含嵌套）、
//             行尾耗时（秒级实数据）、✘ bash bad 态；
//     收口：整泡替换成终文本「树完成。」，trow 清零（零回归）。
//   会话面 → 夹具会话：
//     三个树行（read/bash/xss）行尾带结果规模；tool 消息全数独立成泡
//     （W4.4 逐条渲染零回归）；展开 read 行——参数全文、结果摘要截断
//     （END_MARK 不进 DOM）、「复制全文」把整串交给剪贴板层（钩子
//     __copySink 拿到 END_MARK）；再点行收起；XSS 字面渲染不执行。
//   兜底 → appendMsg 毒消息（getter 抛错）：树→老形态→占位行全灭时落
//     「（消息渲染失败）」，后续消息恢复，页面不白屏。
//
// 用法：node scripts/web-tree-headless.mjs <页面URL> <桩项目路径> <夹具会话sid>
//       [--chrome <chrome 可执行文件>] [--dbg-port <CDP 端口>]
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const opt = (name, dflt) => {
  const i = argv.indexOf(name);
  if (i < 0) return dflt;
  const v = argv[i + 1];
  argv.splice(i, 2);
  return v;
};
const CHROME = opt('--chrome', process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome');
const dbgPort = parseInt(opt('--dbg-port', '18794'), 10);
const pageUrl = argv[0];
const projDir = argv[1];
const treeSid = argv[2];
if (!pageUrl || !projDir || !treeSid) {
  console.error('usage: web-tree-headless.mjs <pageUrl> <projDir> <treeSid> [--chrome path] [--dbg-port n]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const WATCHDOG_MS = 120_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const profile = mkdtempSync(path.join(tmpdir(), 'seek-ux7lvk-chrome.'));
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
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);

// 聊天面快照：流式调用树断言读这一份。
const snapshot = () => evalJS(`(() => ({
  msgs: [...document.querySelectorAll('#list > .msg')].map(el => ({
    txt: el.textContent,
    trows: [...el.querySelectorAll('.trow')].map(t => ({
      txt: t.textContent,
      bad: t.classList.contains('bad'),
      ml: parseFloat(getComputedStyle(t).marginLeft) || 0,
    })),
  })),
  nstream: document.querySelectorAll('.msg.streaming').length,
  ntrow: document.querySelectorAll('.trow').length,
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

// 轮询直到谓词过或超时；history 收集过程态（嵌套/耗时只在过程态可见）。
async function until(what, snap, pred, ms) {
  const t0 = Date.now();
  let s = await snap();
  const history = [s];
  while (!pred(s)) {
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）`);
    await delay(120);
    s = await snap();
    history.push(s);
  }
  return { s, history };
}

// ---- 景 A：流式调用树（live）----
trace('景 A：/bind + 「树」');
try {
  if ((await send(`/bind ${projDir}`)) !== 202) throw new Error('POST /bind 非 202');
  await until('bind 应答', snapshot, s => s.msgs.some(m => m.txt.includes('已绑定项目')), 8000);
  if ((await send('树')) !== 202) throw new Error('POST 非 202');
  const { s: fin, history } = await until('树收口', snapshot,
    s => s.nstream === 0 && s.msgs.some(m => m.txt.includes('树完成。')), 15000);

  // 取「三行俱在」的最后过程帧（收口前一拍：耗时已冻结、缩进仍在）。
  const full = [...history].reverse().find(h => h.ntrow === 3);
  ck('过程态：三行调用树出现', !!full,
    `ntrow 轨迹：${history.map(h => h.ntrow).join(' ')}`);
  if (full) {
    const rows = full.msgs.flatMap(m => m.trows);
    const grep = rows.find(r => r.txt.includes('grep'));
    const read = rows.find(r => r.txt.includes('read'));
    const bash = rows.find(r => r.txt.includes('bash'));
    ck('嵌套：read 缩进一层（区间包含），grep 顶层',
      !!grep && !!read && read.ml > grep.ml && grep.ml === 0,
      JSON.stringify(rows.map(r => [r.txt, r.ml])));
    ck('耗时：grep 行冻结秒级实值（✔ + · N.Ns）',
      !!grep && /✔ grep · \d+\.\d+s/.test(grep.txt), grep && grep.txt);
    ck('耗时：read 行也有冻结值', !!read && /✔ read · \d+\.\d+s/.test(read.txt),
      read && read.txt);
    ck('✘ bash 行 bad 态', !!bash && bash.bad && /^✘ bash/.test(bash.txt),
      bash && JSON.stringify(bash));
  }
  ck('收口：整泡替换终文本（树完成。）+ trow 清零（零回归）',
    fin.ntrow === 0 && fin.msgs.some(m => m.txt.includes('树完成。')),
    `ntrow=${fin.ntrow}`);
} catch (e) { ck('景 A 流式调用树', false, e.message); await dump(); }

// ---- 景 B：会话浏览器调用树（history）----
trace('景 B：会话面板 + 夹具');
try {
  await clickSel('#browse-btn');
  const srows = () => evalJS(`(() => ({
    sessView: document.body.classList.contains('sess-view'),
    rows: [...document.querySelectorAll('#sess-list .sess-row')].map(r => r.dataset.sid),
  }))()`);
  const { s: listed } = await until('会话列表', srows,
    s => s.rows.includes(`${treeSid}`), 10000);
  ck('面板开：夹具会话在列', listed.sessView, '');

  await evalJS(`(document.querySelector(${JSON.stringify(
    `#sess-list .sess-row[data-sid="${treeSid}"]`)})||{click(){}}).click(),'ok'`);
  const treeSnap = () => evalJS(`(() => ({
    tnodes: [...document.querySelectorAll('#sess-msgs .ttree > .tnode')].map(t => ({
      nm: t.querySelector('.nm').textContent,
      sm: t.querySelector('.sm').textContent,
      rs: t.querySelector('.rs').textContent,
    })),
    ntoolBubble: document.querySelectorAll('#sess-msgs .smsg.tool').length,
    nscript: document.scripts.length,
    xssTree: typeof window.__xssTree,
  }))()`);
  const { s: tr } = await until('树行渲染', treeSnap, s => s.tnodes.length >= 3, 10000);
  const [readRow, bashRow, xssRow] = tr.tnodes;
  ck('树行 3 个（read/bash/xss）', tr.tnodes.length === 3,
    JSON.stringify(tr.tnodes.map(t => t.nm)));
  ck('行内参数摘要（read 带 path）', !!readRow && readRow.sm.includes('pkg/agent/agent.go'),
    readRow && readRow.sm);
  ck('结果并入行：read 行尾 5008 字符', !!readRow && readRow.rs.includes('5008'),
    readRow && readRow.rs);
  ck('结果并入行：bash 行尾规模（登记进表）', !!bashRow && /字符/.test(bashRow.rs),
    bashRow && bashRow.rs);
  ck('tool 消息全数成泡（W4.4 逐条渲染零回归，4 个）', tr.ntoolBubble === 4,
    `got ${tr.ntoolBubble}`);
  ck('XSS：args 字面渲染、未执行',
    !!xssRow && xssRow.sm.includes('<script>') && tr.xssTree === 'undefined'
    && tr.nscript === 2,
    JSON.stringify(xssRow) + ` xssTree=${tr.xssTree} nscript=${tr.nscript}`);

  // 展开 read 行：参数全文 + 结果摘要截断（END_MARK 不进 DOM）。
  const openDetail = async () => {
    await evalJS(`[...document.querySelectorAll('#sess-msgs .ttree > .tnode')][0].click(),'ok'`);
    return evalJS(`(() => {
      const d = [...document.querySelectorAll('#sess-msgs .ttree > .tnode')][0]
        .nextElementSibling;
      return {
        hidden: d.hidden,
        labels: [...d.querySelectorAll('.tlabel')].map(l => l.textContent),
        pres: [...d.querySelectorAll('.tpre')].map(p => ({
          len: p.textContent.length, txt: p.textContent,
        })),
        ncopies: d.querySelectorAll('.tcopy').length,
      };
    })()`);
  };
  const d1 = await openDetail();
  ck('展开：面板可见 + 参数/结果两节', !d1.hidden && d1.pres.length === 2,
    JSON.stringify(d1.labels));
  ck('展开：参数全文（含 path）', d1.pres[0].txt.includes('"path"'), d1.pres[0].txt.slice(0, 80));
  ck('截断：结果摘要无 END_MARK（DOM 只装摘要）',
    !d1.pres[1].txt.includes('END_MARK') && d1.pres[1].len <= 4100,
    `len=${d1.pres[1].len}`);
  ck('截断：标签带「共 5008 字符」规模', d1.labels.some(l => l.includes('共 5008 字符')),
    JSON.stringify(d1.labels));

  // 复制全文：两条路径（clipboard API / execCommand 兜底）都把整串交给
  // 剪贴板层——钩子 __copySink 记录实际递交的文本（headless 无剪贴板
  // 权限时 writeText 拒绝 → 兜底路径，钩子同样命中）。
  await evalJS(`(() => {
    window.__copySink = null;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      const o = navigator.clipboard.writeText.bind(navigator.clipboard);
      navigator.clipboard.writeText = t => { window.__copySink = String(t); return o(t); };
    }
    const oe = document.execCommand.bind(document);
    document.execCommand = (c, ...r) => {
      if (c === 'copy') window.__copySink = String((document.activeElement || {}).value || '');
      return oe(c, ...r);
    };
  })();'ok'`);
  await evalJS(`[...document.querySelectorAll('#sess-msgs .ttree > .tnode')][0]
    .nextElementSibling.querySelectorAll('.tcopy')[1].click(),'ok'`);
  await delay(200);
  const sink = await evalJS(`window.__copySink`);
  ck('复制全文：整串递交剪贴板层（含 END_MARK，5008 字符）',
    typeof sink === 'string' && sink.includes('END_MARK') && sink.length === 5008,
    `sink.len=${sink && sink.length}`);

  // 收起：再点行 → hidden。
  await evalJS(`[...document.querySelectorAll('#sess-msgs .ttree > .tnode')][0].click(),'ok'`);
  const hidden = await evalJS(`[...document.querySelectorAll('#sess-msgs .ttree > .tnode')][0]
    .nextElementSibling.hidden`);
  ck('收起：再点行面板隐藏', hidden === true, `got ${hidden}`);
} catch (e) { ck('景 B 会话调用树', false, e.message); await dump(); }

// ---- 景 C：渲染兜底（不白屏）----
trace('景 C：毒消息兜底');
try {
  const fb = await evalJS(`(() => {
    const box = document.getElementById('sess-msgs');
    const before = box.childElementCount;
    appendMsg({ get role() { return 'assistant'; },
                get content() { return 'poison'; },
                get tool_calls() { throw new Error('poison'); } });
    appendMsg({ role: 'assistant', content: 'fallback 之后恢复' });
    const kids = [...box.children];
    return {
      grew: kids.length - before,
      last1: kids.at(-2).textContent,
      last2: kids.at(-1).textContent,
      alive: !!(document.getElementById('status') && document.getElementById('form')
                && document.getElementById('list')),
    };
  })()`);
  ck('兜底：毒消息落占位行「（消息渲染失败）」', fb.last1 === '（消息渲染失败）', fb.last1);
  ck('兜底：后续消息恢复渲染', fb.grew === 2 && fb.last2.includes('fallback 之后恢复'),
    JSON.stringify(fb));
  ck('兜底：页面不白屏（部件俱在）', fb.alive, JSON.stringify(fb));
} catch (e) { ck('景 C 兜底', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（c_ux7lvk 调用树无头验收）');
process.exit(fails.length ? 1 : 0);
