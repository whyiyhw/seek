#!/usr/bin/env node
// web-input-headless.mjs — 输入增强（c_7lsngm）的无头验收半边
// （scripts/web-input-manual.sh 的驱动）。
//
// 与前面几个无头脚本的差别：本卡验收的是**键盘路径**，所以按键走
// CDP Input.dispatchKeyEvent（真按键、真默认行为），而不是 JS 合成
// KeyboardEvent——合成事件没有默认行为，Shift+Enter 到底有没有插进
// 换行这件事只能靠真按键才验得出来。文本输入走 Input.insertText。
//
// 三组路径 + 两条边界：
//   A 多行    Shift+Enter 追加换行且不发送；Enter 发送且草稿清空。
//   B '/'     '/' 弹命令面板（注册表里的 5 条 + /resume）；↑↓ 移动高亮；
//             Tab 补全成「/xxx 」；Esc 关闭且不劫持（关闭态 Esc 的
//             defaultPrevented=false，页面别处照常收键）。
//   C '@'     '@' 弹最近会话（/rpc session/list 的夹具）；Enter 选中后
//             草稿落 @<sid>，并自动发出 /resume <sid>——/state 的会话
//             字段翻成该 sid 即「切换语义」落地。
//   边界    窄屏（390×700）：弹层铺满 composer 内容区、输入 16px、条目可
//             换行，键盘路径仍通（重跑一遍 B 的最短路径）。「满宽」量的是
//             内容区不是视口：窄屏有 56px 图标轨 + 12px 侧留白（W6-8 /
//             c_4wpajv），详见 D 段断言的注（c_rddtrm 修陈旧阈值）。
//
// 用法：node scripts/web-input-headless.mjs <页面URL> <桩项目路径> <会话sid>
//       [--chrome <chrome 可执行文件>]
// 退出码：0 全绿；1 启动/断言失败（stderr 带页面现场）。
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
const projDir = args[1];
const wantSid = args[2];
if (!pageUrl || !projDir || !wantSid) {
  console.error('usage: web-input-headless.mjs <pageUrl> <projDir> <sid> [--chrome path]');
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

// ---- 1. 起 headless Chrome ----
const dbgPort = 18797;   // 与 serve 的监听口分开（本脚本默认 18796 是 serve 的）
const profile = mkdtempSync(path.join(tmpdir(), 'seek-wtin-chrome.'));
const chrome = spawn(CHROME, [
  '--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
  '--disable-extensions', `--remote-debugging-port=${dbgPort}`,
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
  if (chrome.exitCode !== null) break;   // 同步查活，别 await 死亡 Promise
  try {
    const list = await (await fetch(`http://127.0.0.1:${dbgPort}/json/list`,
      { signal: AbortSignal.timeout(2000) })).json();
    // 换过票的页面 URL 会被改写成 "/"（token 已进 cookie），所以不比对
    // 完整路径，只认本次 profile 的首个 page 目标。
    target = list.find(t => t.type === 'page' && t.webSocketDebuggerUrl);
  } catch { /* 还没起 */ }
}
if (!target) {
  console.error('FAIL: 5s 内没抓到 DevTools 目标\n' + chromeErr.join('').split('\n').slice(-5).join('\n'));
  process.exit(1);
}
trace('目标已连：' + target.url);

// ---- 2. CDP 客户端（Runtime.evaluate + Input 域）----
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
function call(method, params) {
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

const KEYS = {
  Enter:       { key: 'Enter', code: 'Enter', vk: 13, text: '\r' },
  Tab:         { key: 'Tab', code: 'Tab', vk: 9 },
  Escape:      { key: 'Escape', code: 'Escape', vk: 27 },
  ArrowDown:   { key: 'ArrowDown', code: 'ArrowDown', vk: 40 },
  ArrowUp:     { key: 'ArrowUp', code: 'ArrowUp', vk: 38 },
  Backspace:   { key: 'Backspace', code: 'Backspace', vk: 8 },
};
// 真按键：keyDown（带 text 的键会触发浏览器默认编辑行为）+ keyUp。
// modifier 8 = Shift（CDP Input 域的位掩码）。
async function press(name, { shift = false } = {}) {
  const k = KEYS[name];
  if (!k) throw new Error('unknown key ' + name);
  const modifiers = shift ? 8 : 0;
  await call('Input.dispatchKeyEvent', {
    type: k.text ? 'keyDown' : 'rawKeyDown',
    key: k.key, code: k.code,
    windowsVirtualKeyCode: k.vk, nativeVirtualKeyCode: k.vk,
    modifiers, ...(k.text ? { text: k.text } : {}),
  });
  await call('Input.dispatchKeyEvent', {
    type: 'keyUp', key: k.key, code: k.code,
    windowsVirtualKeyCode: k.vk, nativeVirtualKeyCode: k.vk, modifiers,
  });
}
const typeText = t => call('Input.insertText', { text: t });

// 页面状态快照：输入框内容/选区、弹层开合与条目、气泡数、绑定态。
const snap = () => evalJS(`(() => {
  const i = document.getElementById('input');
  const m = document.getElementById('menu');
  return {
    val: i ? i.value : null,
    caret: i ? i.selectionStart : -1,
    menuOpen: m ? !m.hidden : false,
    items: m ? [...m.querySelectorAll('.mi')].map(x => ({
      label: x.querySelector('.nm').textContent,
      meta: x.querySelector('.sid') ? x.querySelector('.sid').textContent : '',
      hl: x.classList.contains('hl'),
    })) : [],
    bubbles: document.querySelectorAll('#list > .msg').length,
    sendDisabled: document.getElementById('send')
      ? document.getElementById('send').disabled : null,
    chatEmpty: (document.querySelector('#list .chat-empty') || {}).textContent || '',
    status: document.getElementById('status-l').textContent,
    tag: i ? i.tagName : '',
    focused: document.activeElement ? document.activeElement.id : '',
  };
})()`);
const clearInput = () => evalJS(`(() => {
  const i = document.getElementById('input');
  i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true }));
  i.focus(); return i.value;
})()`);
// 绑定/状态：GET /state —— 必须带页面的 conv 头（CONV），否则读到的是
// 平面的默认会话 web:default，与本页的绑定根本不是一条。
const state = () => evalJS(`fetch(q('/state'),
  { headers: { 'X-Seek-Conversation': CONV } }).then(r => r.json())`);
const bindProject = async () => {
  const st = await evalJS(`fetch(q('/msg'), { method: 'POST',
     headers: { 'Content-Type': 'application/json', 'X-Seek-Conversation': CONV },
     body: JSON.stringify({ text: ${JSON.stringify('/bind ' + projDir)} }) }).then(r => r.status)`);
  if (st !== 202) throw new Error('/bind POST = ' + st);
  for (let i = 0; i < 100; i++) {
    await delay(100);
    const s = await state();
    if (s.bound) return s;
  }
  throw new Error('/bind did not land in /state');
};

// ---- 3. 断言驱动 ----
const fails = [];
const ck = (name, ok, detail) => {
  console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '：' + detail));
  if (!ok) fails.push(name);
};
const dump = async () => { try { console.log(JSON.stringify(await snap(), null, 1)); } catch { /* 页面已关 */ } };

trace('等页面就绪');
for (let i = 0; i < 100; i++) {
  await delay(150);
  let ready = '';
  try {
    ready = await evalJS(`(document.getElementById('status').textContent.includes('连接中') ? '' : '1')`);
  } catch { /* 尚未执行到脚本 */ }
  if (ready) break;
  if (i === 99) { console.error('FAIL: 15s 页面未就绪'); process.exit(1); }
}
const base = await snap();
ck('输入框是 textarea（多行的前提）', base.tag === 'TEXTAREA', `got ${base.tag}`);
ck('基线 scripts=3（head 主题 bootstrap + 页面脚本 + markdown.js）',
  (await evalJS('document.scripts.length')) === 3, '');
ck('基线弹层关闭', !base.menuOpen, JSON.stringify(base));
// ---- 主区空态 + 发送禁用态（c_jf3grt）----
// 未绑定的空聊天面给 /bind 引导（含点选按钮），空草稿时发送键禁用；
// 有草稿即可点，清空回到禁用。这两条是「页面不许给死气/可点的空操作」
// 的可见证据。
ck('主区空态（未绑定）：Hero + 选择工作区芯片（W6-4）',
  base.chatEmpty.includes('seek')
  && base.chatEmpty.includes('选择工作区')
  && (await evalJS(`!!document.querySelector('#list .chat-empty .ce-chip.pick')`)),
  JSON.stringify(base.chatEmpty));
ck('发送键：空草稿缺省禁用（静态标记 + syncSend）',
  base.sendDisabled === true, `disabled=${base.sendDisabled}`);
await evalJS(`document.getElementById('input').focus()`);
await typeText('草稿');
let stDraft = await snap();
ck('发送键：有草稿即可点', stDraft.sendDisabled === false, `disabled=${stDraft.sendDisabled}`);
await clearInput();
stDraft = await snap();
ck('发送键：清空草稿回到禁用', stDraft.sendDisabled === true, `disabled=${stDraft.sendDisabled}`);
await bindProject();
trace('已绑定桩项目');
// /bind 的 ack 自己也是一条气泡：基线必须在它落地之后再取，否则「不清空/
// 不发送」这类断言会把它算成新消息。
base.bubbles = (await snap()).bubbles;
ck('消息落地后空态让位（#list 只剩 .msg）',
  (await snap()).chatEmpty === '', (await snap()).chatEmpty);

// 全局键不劫持的探针：document 级监听记录「最后一个键是否被 preventDefault」。
// 本页的弹层按键全部挂在 input 上并且只在开着时 preventDefault——关掉即
// 完全放行，探针在 document 冒泡末端看得到真相。
await evalJS(`(() => {
  window.__keys = [];
  document.addEventListener('keydown', ev => {
    window.__keys.push({ key: ev.key, prevented: ev.defaultPrevented });
  });
  return 1;
})()`);
const lastKey = () => evalJS(`window.__keys[window.__keys.length - 1] || null`);

// ---- A. 多行：Shift+Enter 换行、Enter 发送 ----
trace('A 多行输入');
await evalJS(`document.getElementById('input').focus()`);
await clearInput();
await typeText('第一行');
await press('Enter', { shift: true });
await typeText('第二行');
let s = await snap();
ck('Shift+Enter 插入换行且未发送',
  s.val === '第一行\n第二行' && s.bubbles === base.bubbles,
  JSON.stringify({ val: s.val, bubbles: s.bubbles }));
ck('Shift+Enter 未被 preventDefault（原生换行）',
  !(await lastKey()).prevented, JSON.stringify(await lastKey()));

await press('Enter');
for (let i = 0; i < 60; i++) {
  await delay(100);
  s = await snap();
  if (s.val === '' && s.bubbles > base.bubbles) break;
}
ck('Enter 发送（气泡 +1）且草稿清空',
  s.val === '' && s.bubbles === base.bubbles + 1, JSON.stringify({ val: s.val, bubbles: s.bubbles }));
ck('Enter 被 preventDefault（不插换行）',
  (await lastKey()).prevented, JSON.stringify(await lastKey()));
const sent = await evalJS(`document.querySelector('#list > .msg:last-child').textContent`);
ck('发送内容是两行原文', sent.includes('第一行') && sent.includes('第二行'), JSON.stringify(sent));

// ---- B. '/' 指令补全：面板、↑↓、Tab、Esc、不劫持 ----
trace("B '/' 指令补全");
const bubblesNow = (await snap()).bubbles;
{
  await evalJS(`document.getElementById('input').focus()`);
  await clearInput();
  await typeText('/');
  s = await snap();
  const labels = s.items.map(i => i.label);
  // 注册表是残差表（工作区树卡）：项目/会话导航搬进左栏两级树，只剩树上
  // 没有对应手势的两条。/bind /projects /switch /new 仍是平面指令，但页面
  // 不再列出、也不再提供入口——面板里多出它们就是入口回流。
  ck("'/' 弹命令面板", s.menuOpen && labels.length === 2, JSON.stringify(s.items));
  ck(' 面板恰含 /resume + /cancel', labels.includes('/resume') && labels.includes('/cancel'),
    JSON.stringify(labels));
  ck(' 面板不再列 /bind /projects /switch /new',
    !['/bind', '/projects', '/switch', '/new'].some(n => labels.includes(n)),
    JSON.stringify(labels));
  ck(' 首项默认高亮', s.items[0] && s.items[0].hl, JSON.stringify(s.items));
  await press('ArrowDown');
  s = await snap();
  ck('↑↓ 移动高亮（Down → 第二项）',
    s.items[1] && s.items[1].hl && !s.items[0].hl, JSON.stringify(s.items));
  ck('↑↓ 不写进输入框（按键被消化）', s.val === '/', JSON.stringify(s.val));
  await press('ArrowUp');
  s = await snap();
  ck('↑ 回到首项（循环边界）', s.items[0] && s.items[0].hl, JSON.stringify(s.items));

  // Tab 补全首项：/resume 带参 → 落「/resume 」并留空格；参数期不再有
  // 目录浏览入口（那已经搬去侧栏头的「工作区」钮），所以面板关掉。
  await press('Tab');
  s = await snap();
  ck('Tab 补全首项为「/resume 」', s.val === '/resume ', JSON.stringify(s));
  ck('Tab 被 preventDefault（焦点不跳出输入框）',
    (await lastKey()).prevented && s.focused === 'input', JSON.stringify(await lastKey()));

  // Esc：先重开面板，再 Esc 关闭，且 document 看到的 defaultPrevented=真；
  // 关闭态再按一次 Esc，则 false（不劫持）。
  await clearInput();
  await typeText('/');
  s = await snap();
  ck('回到 \'/\' 重新弹面板', s.menuOpen, JSON.stringify(s));
  await press('Escape');
  s = await snap();
  ck('Esc 关闭面板（草稿保留）', !s.menuOpen && s.val === '/', JSON.stringify(s));
  ck('Esc（面板开）被就地消化', (await lastKey()).prevented, JSON.stringify(await lastKey()));
  await press('Escape');
  s = await snap();
  ck('Esc（面板关）不劫持全局键', !(await lastKey()).prevented, JSON.stringify(await lastKey()));
  ck('关闭态 Esc 不动草稿', s.val === '/', JSON.stringify(s.val));

  // 关闭面板后 Enter 仍是发送（不是补全）：发出去的是字面 '/'。
  // 一条消息会落两条气泡（you + 应答），所以只断言「多了且草稿清空」。
  await press('Enter');
  for (let i = 0; i < 60; i++) {
    await delay(100);
    s = await snap();
    if (s.val === '' && s.bubbles > bubblesNow) break;
  }
  ck('面板关闭后 Enter 走发送路径', s.val === '' && s.bubbles > bubblesNow,
    JSON.stringify({ val: s.val, bubbles: s.bubbles }));

  // 等这一跑的回写落定再进 C：Handle 每个 inbound 一个 goroutine，命令
  // 不走执行器闸，所以 /resume 可能抢在在跑的 prompt 之前落绑定——随后
  // prompt 的 session 回写把它盖掉（/new /switch 同款既有暴露）。C 要断言
  // 「/resume 之后绑定等于夹具 sid」，就必须先让上一跑收口：应答气泡落地
  // = agent_end 已回、回写已完成。
  for (let i = 0; i < 80; i++) {
    s = await snap();
    if (s.bubbles >= bubblesNow + 2) break;   // you + reply
    await delay(100);
  }
  ck('上一跑已收口（回写落定后再验 @）', s.bubbles >= bubblesNow + 2,
    JSON.stringify({ bubbles: s.bubbles, want: bubblesNow + 2 }));
}

// ---- C. '@' 会话引用：候选来自 session/list，选中即落引用 + 切换语义 ----
trace("C '@' 会话引用");
{
  await evalJS(`document.getElementById('input').focus()`);
  await clearInput();
  await typeText('看看 @');
  // 数据是异步拉的（首次触发才请求 session/list）：轮询到弹出为止。
  let got = null;
  for (let i = 0; i < 60; i++) {
    s = await snap();
    if (s.menuOpen && s.items.length) { got = s; break; }
    await delay(100);
  }
  ck("'@' 弹最近会话", !!got, JSON.stringify(s));
  if (got) {
    const metas = got.items.map(i => i.meta);
    ck(' 候选来自 session/list（夹具 sid 在列表里）',
      metas.includes(wantSid.slice(0, 8)), JSON.stringify(got.items));
    // 首项 = 最新会话；夹具只有一条活跃的，Enter 选它。
    const want = got.items.findIndex(i => i.meta === wantSid.slice(0, 8));
    for (let i = 0; i < Math.max(0, want); i++) await press('ArrowDown');
    await press('Enter');
    for (let i = 0; i < 80; i++) {
      await delay(100);
      const st = await state();
      if (st.session === wantSid) break;
    }
    s = await snap();
    const st = await state();
    ck('选中后草稿落 @<sid> 引用', s.val.includes('@' + wantSid), JSON.stringify(s.val));
    ck('选中即发 /resume（绑定会话翻成该 sid）', st.session === wantSid, JSON.stringify(st));
    ck('绑定项目随会话（= 夹具 cwd）', st.project === projDir, JSON.stringify(st));
    ck('会话命令气泡 +1（/resume 被发送）', s.bubbles > bubblesNow + 1, JSON.stringify(s.bubbles));
  }
}

// ---- D. 窄屏降级：弹层铺满内容区、16px 输入、条目可换行，键盘路径仍通 ----
trace('D 窄屏 390×700');
await call('Emulation.setDeviceMetricsOverride',
  { width: 390, height: 700, deviceScaleFactor: 2, mobile: true });
await delay(150);
{
  await evalJS(`document.getElementById('input').focus()`);
  await clearInput();
  await typeText('/');
  s = await snap();
  ck('窄屏下 / 面板照常弹出', s.menuOpen && s.items.length === 2, JSON.stringify(s));
  const box = await evalJS(`(() => {
    const m = document.getElementById('menu');
    const i = document.getElementById('input');
    const cs = getComputedStyle(i);
    const r = m.getBoundingClientRect();
    const f = document.getElementById('form');   // 弹层的定位父级（composer 卡片，W6-4）
    return { w: Math.round(r.width), vw: innerWidth, fs: cs.fontSize,
             wrap: getComputedStyle(m.querySelector('.mi')).flexWrap,
             h: Math.round(i.getBoundingClientRect().height),
             cw: f.clientWidth,   // 内容区宽（边框内）= 「满宽」的比较基准
             sw: document.documentElement.scrollWidth };
  })()`);
  // 「满宽」= 铺满弹层的定位父级（composer 卡片的内容区），不是整幅视口：
  // 窄屏有 56px 图标轨（W6-8 侧栏收起态）+ composer 左右各 12px 留白
  // （c_4wpajv 把 width:100% 换成 calc(100% - 24px)），内容区只有
  // 390-56-24=310 外框 / 308 内容宽——旧阈值「> 视口 80%」（312）是按
  // 「composer 铺满视口」量出来的，轨与留白落地后必红（c_rddtrm 复跑实测
  // w=308 cw=308 sw=390）。改为量相对内容区：差值 ≤2px（取整容差）即铺满，
  // 同时保留「不超视口 / 文档无横向溢出」两条硬边界——丢回桌面的
  // left/right:8px 会让 w=292，这条仍然抓得住。
  ck('弹层铺满内容区（无横向溢出）',
    box.w <= box.vw && Math.abs(box.w - box.cw) <= 2 && box.sw <= box.vw,
    JSON.stringify(box));
  ck('输入字号 16px（防 iOS 聚焦缩放）', box.fs === '16px', JSON.stringify(box));
  ck('条目可换行（窄屏不挤成一行）', box.wrap === 'wrap', JSON.stringify(box));
  await press('Tab');
  s = await snap();
  ck('窄屏下 Tab 仍能补全', s.val === '/resume ' && s.caret === 8, JSON.stringify(s));
}
await call('Emulation.clearDeviceMetricsOverride');

console.log(fails.length ? `\n${fails.length} 项断言失败` : '\n全部断言通过（c_7lsngm 输入增强无头验收）');
process.exit(fails.length ? 1 : 0);
