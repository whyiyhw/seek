#!/usr/bin/env node
// web-subagents-headless.mjs — 子代理面板升级（卡 c_4doebx）的无头浏览器
// 验收半边（scripts/web-subagents-manual.sh 的驱动）。假数据驱动：夹具是
// 手写的 subagents.jsonl 事件行 + 一条子 transcript，不 spawn 真子代理。
//
// 零依赖：Node ≥22（全局 fetch + WebSocket），经 Chrome DevTools 协议驱动
// 本机 headless Chrome。全部交互走真实 UI 路径（按钮 .click()——与真人点
// 击同一条线），脚本不持有 token（URL 由外层脚本传入）。
//
// 断言五景（对应卡面验收 ②③ 与 manual 清单 1–5）：
//   进面板   → 📚 会话让位给 🤖 后台；三行记录（服务端 newest-first）
//   三态徽标 → 「在跑」= st-running、「完成」= st-done、「失败」= st-error，
//              dataset.status 保留折叠原值（badge 与行两处一致）
//   详情展开 → 点「完成」行：字段表（sub_sid/parent_sid/状态/类型/模式/
//              开始/tokens/项目）+ 末条 assistant 正文（与 /subagents?sub=
//              同信息量）
//   定时刷新 → 外层脚本在无头阶段落盘 late-1 两行；面板不点任何东西，
//              10s 定时器自己把它拉进列表（agLoads 递增为机械证据）
//   回聊天   → 💬 聊天：聊天面/输入框原样可见（旧端点与聊天面零回归）
//
// 用法：node scripts/web-subagents-headless.mjs <页面URL> <项目路径>
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
  console.error('usage: web-subagents-headless.mjs <pageUrl> <projDir> [--chrome path]');
  process.exit(1);
}
if (typeof WebSocket !== 'function') {
  console.error('need Node ≥22 (global WebSocket) — 或改跑人工清单');
  process.exit(1);
}
// 总闸：定时刷新那一景最长等 25s，其余各景秒级——60s 足够，卡死即失败。
const WATCHDOG_MS = 90_000;
const watchdog = setTimeout(() => {
  console.error(`FAIL: watchdog ${WATCHDOG_MS}ms——驱动卡死（见上面最后一条 trace）`);
  process.exit(1);
}, WATCHDOG_MS);
watchdog.unref();

// ---- 1. 起 headless Chrome，抓调试目标 ----
const dbgPort = 18797;   // W2.1=18792 / W4.4=18793 / W5md=18794；这里错开
const profile = mkdtempSync(path.join(tmpdir(), 'seek-c4doebx-chrome.'));
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

// 面板快照：全部断言读这一份。
const snapshot = () => evalJS(`(() => ({
  browsing: document.body.classList.contains('browsing'),
  agentsView: document.body.classList.contains('agents-view'),
  formShown: getComputedStyle(document.getElementById('form')).display !== 'none',
  status: document.getElementById('status-l').textContent,
  hint: document.querySelector('#ag-list .pane-hint')?.textContent || '',
  rows: [...document.querySelectorAll('#ag-list .ag-row')].map(r => ({
    sid: r.dataset.sid,
    status: r.dataset.status,
    cur: r.classList.contains('cur'),
    badgeText: r.querySelector('.ag-badge')?.textContent || '',
    badgeCls: r.querySelector('.ag-badge')?.className || '',
    badgeKind: r.querySelector('.ag-badge')?.dataset.kind || '',
    badgeStatus: r.querySelector('.ag-badge')?.dataset.status || '',
    desc: r.querySelector('.desc')?.textContent || '',
  })),
  meta: document.getElementById('ag-meta').textContent,
  fields: [...document.querySelectorAll('#ag-fields .ag-field')].map(f => ({
    k: f.querySelector('.k')?.textContent || '',
    v: f.querySelector('.v')?.textContent || '',
  })),
  last: document.getElementById('ag-last').textContent,
  loads: typeof agLoads === 'number' ? agLoads : -1,
  timerOn: agTimer !== null,
  nscript: document.scripts.length,
}))()`);
const clickSel = sel => evalJS(`(document.querySelector(${JSON.stringify(sel)})||{click(){}}).click(),'ok'`);
const clickRow = sid => clickSel(`#ag-list .ag-row[data-sid="${sid}"]`);
const send = text => evalJS(
  `fetch(q('/msg'), { method: 'POST',
     headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
     body: JSON.stringify({ text: ${JSON.stringify(text)} }) }).then(r => r.status)`);

// ---- 3. 场景驱动 + 断言 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snapshot(), null, 1)); } catch { /* 页面已关 */ } };

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

// 就绪：/state 探测落定。
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
ck('基线 scripts=2（内联页面脚本 + static/markdown.js）', base.nscript === 2, `got ${base.nscript}`);
ck('基线在聊天面（未 browsing）', !base.browsing && base.formShown, '');

// 景 0：/bind —— 面板数据源要先有绑定项目。
trace('景 0：/bind');
try {
  if ((await send(`/bind ${projDir}`)) !== 202) throw new Error('POST /bind 非 202');
  const s = await until('bind 应答', s => s.status.includes('已绑定'), 8000);
  ck('/bind 落地：状态行显示已绑定', s.status.includes('已绑定'), s.status);
} catch (e) { ck('/bind 落地', false, e.message); await dump(); }

// 景 1：进面板 —— 三行记录 + 三态徽标（验收 ② 的徽标半边）。
trace('景 1：进面板 + 三态徽标');
try {
  await clickSel('#agents-btn');
  const s = await until('面板三行', s => s.agentsView && s.rows.length === 3, 12000);
  ck('面板开：agents-view + browsing', s.agentsView && s.browsing, '');
  ck('聊天输入框让位（display:none）', !s.formShown, '');
  ck('定时刷新已挂（agTimer 非空）', s.timerOn, '');
  ck('三行 newest-first（fail-1, bg-1, done-1）',
    JSON.stringify(s.rows.map(r => r.sid)) === JSON.stringify(['fail-1', 'bg-1', 'done-1']),
    JSON.stringify(s.rows.map(r => r.sid)));
  const by = Object.fromEntries(s.rows.map(r => [r.sid, r]));
  ck('在跑行徽标 = running 类 + 「在跑」',
    by['bg-1'].badgeKind === 'running' && by['bg-1'].badgeCls.includes('st-running')
    && by['bg-1'].badgeText.includes('在跑'), JSON.stringify(by['bg-1']));
  ck('完成行徽标 = done 类 + 「完成」',
    by['done-1'].badgeKind === 'done' && by['done-1'].badgeCls.includes('st-done')
    && by['done-1'].badgeText.includes('完成'), JSON.stringify(by['done-1']));
  ck('失败行徽标 = error 类 + 「失败」',
    by['fail-1'].badgeKind === 'error' && by['fail-1'].badgeCls.includes('st-error')
    && by['fail-1'].badgeText.includes('失败'), JSON.stringify(by['fail-1']));
  ck('徽标 dataset.status 保留折叠原值（行与徽标一致）',
    s.rows.every(r => r.status === r.badgeStatus && r.status !== ''), JSON.stringify(s.rows));
  ck('后台行带 bg· 前缀', by['bg-1'].badgeText.includes('bg·'), by['bg-1'].badgeText);
  ck('行描述来自索引', by['done-1'].desc === 'find handlers', by['done-1'].desc);
} catch (e) { ck('进面板 + 徽标', false, e.message); await dump(); }

// 景 2：详情展开 —— 字段表 + transcript 尾（与 /subagents?sub= 同信息量）。
trace('景 2：详情展开');
try {
  await clickRow('done-1');
  const s = await until('详情渲染', s => s.fields.length > 0 && s.last.includes('final findings'), 8000);
  const f = Object.fromEntries(s.fields.map(x => [x.k, x.v]));
  ck('详情行高亮（cur）', s.rows.find(r => r.sid === 'done-1').cur, '');
  ck('meta 一行含状态/类型/描述', s.meta.includes('完成') && s.meta.includes('explore')
    && s.meta.includes('find handlers'), s.meta);
  ck('字段表 sub_sid/parent_sid', f['sub_sid'] === 'done-1' && f['parent_sid'] === 'sess-A', JSON.stringify(f));
  ck('字段表 tokens（p/cache_hit）', (f['tokens'] || '').includes('p100') && (f['tokens'] || '').includes('ch80'), f['tokens']);
  ck('字段表 模式=同步', f['模式'] === '同步', f['模式']);
  ck('字段表 项目根', (f['项目'] || '').includes(projDir), f['项目']);
  ck('transcript 尾条正文渲染', s.last.includes('panel e2e: final findings'), s.last.slice(0, 120));

  // 在跑行：详情说明「还在跑」，不是报错。
  await clickRow('bg-1');
  const s2 = await until('在跑详情', s => s.fields.length > 0 && s.meta.includes('在跑'), 8000);
  ck('在跑详情：提示尚无输出（不是错误）',
    s2.last.includes('还在跑') || s2.last.includes('尚无'), s2.last.slice(0, 120));
} catch (e) { ck('详情展开', false, e.message); await dump(); }

// 景 3：刷新 —— 外层脚本已落盘 late-1，两条刷新路径都要有机械证据：
//   ① 事件驱动：subagents topic 的推送（SSE 连接在面板打开时已挂）；
//   ② 定时：AG_REFRESH_MS(10s) 的 ticker 自己再拉一次快照——agLoads 是
//      每次 loadAgents 的自增计数，它涨了就是定时器真的在拉（推送不涨
//      这个数，所以这是定时器的专属证据）。两条各等各的窗口，不互相
//      掩盖：推送通常秒级到，定时器要等满一个 10s 周期。
trace('景 3：刷新（事件驱动 + 定时）');
try {
  const before = await snapshot();
  const s = await until('late-1 自动出现', s => s.rows.some(r => r.sid === 'late-1'), 30000);
  const late = s.rows.find(r => r.sid === 'late-1');
  ck('第 4 行自动出现（未点任何东西）', !!late, JSON.stringify(s.rows.map(r => r.sid)));
  ck('新行徽标 = error（failed/canceled）', late.badgeKind === 'error' && late.badgeText.includes('失败'),
    JSON.stringify(late));
  ck('新行插在 newest-first 首位', s.rows[0].sid === 'late-1', JSON.stringify(s.rows.map(r => r.sid)));
  const ticked = await until('定时器 tick', s => s.loads > before.loads, 25000);
  ck('定时器自己拉了一次快照（agLoads 递增）', ticked.loads > before.loads,
    `before=${before.loads} after=${ticked.loads}`);
} catch (e) { ck('刷新（事件驱动 + 定时）', false, e.message); await dump(); }

// 景 4：回聊天 —— 旧端点与聊天面零回归（验收 ③ 的 UI 半边）。
trace('景 4：回聊天');
try {
  await clickSel('#agents-btn');
  const s = await until('回聊天', s => !s.agentsView && s.formShown, 5000);
  ck('回聊天：聊天面/输入框恢复', s.formShown && !s.browsing, '');
  ck('定时器已停（离面板不空转）', !s.timerOn, '');
  if ((await send('hello')) !== 202) throw new Error('POST /msg 非 202');
  ck('聊天通道仍可用（POST /msg 202）', true, '');
} catch (e) { ck('回聊天', false, e.message); await dump(); }

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（c_4doebx 子代理面板无头验收）');
process.exit(fails.length ? 1 : 0);
