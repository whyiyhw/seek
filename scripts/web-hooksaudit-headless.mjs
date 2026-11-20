#!/usr/bin/env node
// web-hooksaudit-headless.mjs — W5 卡 c_xz2bey hooks 实时审计视图的无头
// 浏览器验收半边（scripts/web-hooksaudit-manual.sh 的驱动）。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱
// 动本机 headless Chrome；审计行由本脚本直接 append 进审计日志文件（就
// 是子进程 seek 的落盘动作）。杀 serve 演练由 --kill/--start 两条 shell
// 命令承载：kill 真停 serve，start 拉起第二个 serve（同 SEEK_HOME 同口
// 同 token），验「断线期间追加的行不丢——重连后自动补拉」。
//
// 断言六景：
//   快照     → 开面板即见已落盘行（hooks/audit/tail），贴底
//   滚动追加 → 追加一行 → 无刷新滚出、仍贴底（验收③交互一）
//   过滤     → 过滤框「lint」只剩 lint 行（shown/held 计数）；清空复原
//              （验收③交互三）
//   暂停/恢复 → 暂停后追加两行：画面冻结 + 「2 新行」计数；恢复补齐贴底
//              （验收③交互二）
//   上翻     → 滚到顶再追加：不拽回 + 新行计数；点计数跳底
//   断线补拉 → kill serve → 追加两行 → 重启 → 自动重连后两行补上，
//              不丢不重（验收②页面半边）
//
// 用法：node web-hooksaudit-headless.mjs <页面URL> <审计日志路径>
//       --kill <cmd> --start <cmd> [--chrome <path>]
// 退出码：0 全部断言过；1 启动/断言失败（stderr 带 DOM 现场）。
import { spawn } from 'node:child_process';
import { appendFileSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

const argv = process.argv.slice(2);
const trace = m => console.error('[trace] ' + m);
const opt = (name) => {
  const i = argv.indexOf(name);
  const v = i > 0 ? argv[i + 1] : undefined;
  if (i > 0) { argv.splice(i, 2); }
  return v;
};
const CHROME = opt('--chrome') || process.env.CHROME
  || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const KILL_CMD = opt('--kill');
const START_CMD = opt('--start');
const pageUrl = argv[0];
const auditPath = argv[1];
if (!pageUrl || !auditPath || !KILL_CMD || !START_CMD) {
  console.error('usage: web-hooksaudit-headless.mjs <pageUrl> <auditPath> --kill <cmd> --start <cmd> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
const sh = cmd => spawn('sh', ['-c', cmd], { stdio: ['ignore', 'ignore', 'inherit'] });

const WATCHDOG_MS = 150_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// 审计行铸造：hook/session 可指定，ts 单调递增（同秒多行也不撞整行键）。
let seq = 0;
const mint = (hook, session, extra = {}) => JSON.stringify({
  ts: new Date(Date.now() + (seq++) * 1000).toISOString().replace(/\.\d+Z$/, 'Z'),
  event: 'post_tool_use', hook, tool: 'bash', session_id: session,
  duration_ms: 5 + seq, exit_code: 0, ...extra,
});
const appendAudit = line => appendFileSync(auditPath, line + '\n');

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18799;   // 18792–18798 既有脚本占用
const profile = mkdtempSync(path.join(tmpdir(), 'seek-w5hk-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${dbgPort}`,
  `--user-data-dir=${profile}`, '--window-size=1280,900', pageUrl,
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
let cdpSeq = 0;
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
    const id = ++cdpSeq;
    pending.set(id, { res, rej });
    ws.send(JSON.stringify({ id, method: 'Runtime.evaluate',
      params: { expression: expr, awaitPromise: true, returnByValue: true } }));
  });
  if (r.exceptionDetails) {
    throw new Error('page eval failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  }
  return r.result.value;
}

// 审计面板的页面快照：面板态/渲染行数/持有数/冻结/计数/贴底/滚动位。
// sessions 取自页面状态（行文本不渲染 session_id/reason——reason 在
// title 悬停里），断言内容在场/恰一次用它，不看 logText。
const hkSnap = () => evalJS(`(() => {
  const log = document.getElementById('hk-log');
  const rows = log.querySelectorAll('.hk-row');
  return {
    paneOpen: document.body.classList.contains('hooks-view'),
    rendered: rows.length,
    held: hkRows.length,
    frozen: hkFreeze !== null,
    cnt: document.getElementById('hk-cnt').textContent,
    atBottom: log.scrollHeight - log.scrollTop - log.clientHeight < 24,
    scrollTop: log.scrollTop,
    logText: log.textContent,
    sessions: hkRows.map(r => r.session_id),
  };
})()`);
const click = id => evalJS(`document.getElementById('${id}').click()`);
const setFilter = v => evalJS(`(() => {
  const f = document.getElementById('hk-filter');
  f.value = ${JSON.stringify(v)};
  f.dispatchEvent(new Event('input'));
  return f.value;
})()`);

// ---- 3. 景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await hkSnap(), null, 1)); } catch { /* 页面已关 */ } };
async function until(what, pred, ms) {
  const t0 = Date.now();
  let s = await hkSnap();
  while (!pred(s)) {
    if (Date.now() - t0 > ms) throw new Error(`等 ${what} 超时（${ms}ms）：${JSON.stringify(s)}`);
    await delay(150);
    s = await hkSnap();
  }
  return s;
}
// 出现次数：文本按子串计，数组按元素精确计（session 唯一，二者等价）。
const hits = (t, sub) => typeof t === 'string'
  ? t.split(sub).length - 1
  : t.filter(x => x === sub).length;

trace('等页面就绪（SSE 判定过鉴权并已挂流）');
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = false;
  try {
    ready = await evalJS(`(!document.getElementById('status').textContent.includes('连接中'))`);
  } catch { /* 页面尚未执行到脚本，继续轮询 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}

// 景 1：开面板——快照落地（manual.sh 预置 40 垫高 + fmt/lint/ctx + 自检
// live1），贴底。基线行数不写死：后续全部按 base 增量断言。
trace('景 1：开面板取快照');
let base = 0;
try {
  await click('hk-btn');
  const s = await until('快照落地', s => s.paneOpen && s.rendered >= 44, 10_000);
  base = s.rendered;
  ck('快照：全部已落盘行（含垫高与 live1）',
    s.rendered === s.held && s.sessions.includes('live1'), `rendered=${s.rendered} held=${s.held}`);
  ck('快照：贴底（滚动追加的起点）', s.atBottom, '');
  ck('面板：无需 /bind 即可开（全局审计）', s.paneOpen, '');
} catch (e) { ck('开面板取快照', false, e.message); await dump(); }

// 景 2：滚动追加——追加一行，无刷新滚出、仍贴底（验收③交互一）。
trace('景 2：滚动追加');
try {
  appendAudit(mint('fmt', 'live-A'));
  const s = await until('新行滚出', s => s.rendered === base + 1, 5_000);
  ck('滚动追加：新行无刷新出现', s.rendered === base + 1 && s.sessions.includes('live-A'),
    `rendered=${s.rendered}`);
  ck('滚动追加：自动贴底', s.atBottom, '');
} catch (e) { ck('滚动追加', false, e.message); await dump(); }

// 景 3：过滤——「lint」只剩 lint 行、计数 shown/held；清空复原（验收③交互三）。
trace('景 3：按 hook 名过滤');
try {
  await setFilter('lint');
  const s = await until('过滤生效', s => s.rendered === 1, 3_000);
  ck('过滤：只剩 lint 行（行文本即 hook 名）', s.rendered === 1 && s.logText.includes('lint'), `rendered=${s.rendered}`);
  ck('过滤：计数 shown/held', s.cnt.startsWith('1/'), `cnt=${s.cnt}`);
  await setFilter('LINT');   // 大小写不敏感
  const ci = await until('大小写不敏感', s => s.rendered === 1, 2_000);
  ck('过滤：大小写不敏感', ci.rendered === 1, `rendered=${ci.rendered}`);
  await setFilter('');
  const back = await until('清空复原', s => s.rendered === base + 1, 3_000);
  ck('过滤：清空复原', back.rendered === base + 1, `rendered=${back.rendered}`);
} catch (e) { ck('按 hook 名过滤', false, e.message); await dump(); }

// 景 4：暂停/恢复——冻结渲染不冻结积累；恢复补齐（验收③交互二）。
trace('景 4：暂停/恢复');
try {
  await click('hk-pause');
  let s = await until('暂停冻结', s => s.frozen, 2_000);
  appendAudit(mint('fmt', 'pause-1'));
  appendAudit(mint('ctx', 'pause-2', { event: 'pre_prompt_inject', injected_bytes: 42 }));
  await delay(1_800);   // 服务端 1s poll + 推送余量
  s = await hkSnap();
  ck('暂停：画面冻结（行数不动）', s.rendered === base + 1, `rendered=${s.rendered}`);
  ck('暂停：计数出现 2 新行', s.cnt.includes('2 新行'), `cnt=${s.cnt}`);
  await click('hk-pause');   // 恢复
  s = await until('恢复补齐', s => !s.frozen && s.rendered === base + 3, 3_000);
  ck('恢复：补齐并贴底', s.rendered === base + 3 && s.atBottom, `rendered=${s.rendered}`);
} catch (e) { ck('暂停/恢复', false, e.message); await dump(); }

// 景 5：上翻不拽回——滚到顶追加：视图不动 + 新行计数；点计数跳底。
trace('景 5：上翻不拽回');
try {
  await evalJS(`document.getElementById('hk-log').scrollTop = 0`);
  appendAudit(mint('fmt', 'scroll-top'));
  await delay(1_800);
  let s = await hkSnap();
  ck('上翻：不被新行拽回（scrollTop≈0）', s.scrollTop < 10 && !s.atBottom, `scrollTop=${s.scrollTop}`);
  ck('上翻：新行计数提示', s.cnt.includes('新行'), `cnt=${s.cnt}`);
  await click('hk-cnt');   // fresh 态可点跳底
  s = await until('跳底', s => s.atBottom, 2_000);
  ck('上翻：点计数跳底见新行', s.atBottom && s.sessions.includes('scroll-top'), '');
} catch (e) { ck('上翻不拽回', false, e.message); await dump(); }

// 景 6：断线补拉——杀 serve、断线期间追加两行、重启：自动重连 + 补拉，
// 行不丢不重（验收②页面半边；服务端半边在 Go 测试 TestWeb_TopicHooks*）。
trace('景 6：断线重连不丢行');
try {
  sh(KILL_CMD);
  await delay(1_200);   // 断线被页面感知（灯转黄，EventSource 进退避）
  appendAudit(mint('fmt', 'outage-1'));
  appendAudit(mint('lint', 'outage-2'));   // 服务端不在，落盘照常
  sh(START_CMD);
  const s = await until('重连补拉', s => s.rendered === base + 6, 20_000);
  ck('断线补拉：断线期间两行补上（+6=全程增量）', s.rendered === base + 6 && s.held === base + 6,
    `rendered=${s.rendered} held=${s.held}`);
  ck('断线补拉：两行内容在、且各恰一次',
    hits(s.sessions, 'outage-1') === 1 && hits(s.sessions, 'outage-2') === 1,
    `sessions=${s.sessions.slice(-6).join(',')}`);
  ck('断线补拉：贴底恢复', s.atBottom, '');
} catch (e) { ck('断线重连不丢行', false, e.message); await dump(); }

// ---- 收口 ----
if (fails.length) {
  console.error(`FAIL: ${fails.length} 项断言未过：${fails.join('、')}`);
  process.exit(1);
}
console.log('W5 c_xz2bey hooks 审计视图：无头断言全部通过');
process.exit(0);
