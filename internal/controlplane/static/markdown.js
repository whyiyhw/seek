'use strict';
// seek web — 消息正文 markdown 子集渲染器（W5 卡 c_x2au4t，feature-web-
// frontend §13.1 markdown 行）。static/ 拆分的第一个非内联资产：页面仍单
// HTML，本文件由 /static/ 同源自供，CSP 的 script-src 'self' 直接覆盖它
// （内联块才要 hash 钉）。
//
// 选型记录（为何手写而非 vendor）：marked ~39KB / markdown-it ~110KB 源字
// 节双双超 ≤25KB 配额（§13.3 按 embed 源字节 1:1 计），且 HTML-string 输
// 出必然要把字符串解析进 DOM——违反 §5.5「模型输出永不进 HTML 解析
// 路径」的既有纪律。手写子集两头都占：线性一趟、无回溯正则，DOM API
// 白名单构造。覆盖：围栏代码块、行内码、标题（#..######）、无序/有序列
// 表、链接；其余原样纯文本（换行交给容器的 pre-wrap）。
//
// XSS 纪律（转义优先 + 标签白名单）：文本一律 createTextNode/textContent
// ——浏览器侧即为转义，不存在「先拼 HTML 再过滤」的窗口；元素构造仅限
// PRE/CODE/UL/OL/LI/H1-H6/A 白名单；链接 href 过协议白名单（http/https/
// mailto + 相对路径按页面源解析），javascript:/data: 一类整链按字面渲染
// 为文本。整页不存在 HTML 字符串解析路径（页面与 W2.1 静态断言同款纪律）。
//
// 流式语义（W2.1 合帧渲染的渲染半边）：纯函数、单趟、每帧对累积全文重
// 渲的代价与文本长度线性相关（聊天级几 KB 亚毫秒）；未闭合围栏吞到文本
// 末尾、未配对 ` 或 [ 保持字面——delta 半截文本天然可渲，reply 收口再全
// 量重渲成权威形状。
(function () {
  // ---- 行内：`code` 与 [text](url)，从左到右先到先得 ----
  // 链接目标不收空白/括号（嵌套括号的 URL 不支持——子集口径）；链接文字
  // 本身按纯文本走（不递归行内嵌套）。
  var LINK_RE = /^\[([^\]]*)\]\(([^()\s]*)\)/;
  var SAFE_PROTO = { 'http:': 1, 'https:': 1, 'mailto:': 1 };

  function safeHref(url) {
    var u;
    try { u = new URL(url, location.href); } catch (e) { return null; }
    return SAFE_PROTO[u.protocol] ? u.href : null;
  }

  function appendInline(parent, text) {
    for (;;) {
      var t = text.indexOf('`');
      var b = text.indexOf('[');
      if (t < 0 && b < 0) break;
      if (b >= 0 && (t < 0 || b < t)) {
        var m = LINK_RE.exec(text.slice(b));
        if (!m) {  // 不是链接：这个 '[' 按字面出，继续扫余下
          parent.appendChild(document.createTextNode(text.slice(0, b + 1)));
          text = text.slice(b + 1);
          continue;
        }
        if (b > 0) parent.appendChild(document.createTextNode(text.slice(0, b)));
        var href = safeHref(m[2]);
        if (href) {
          var a = document.createElement('a');
          a.href = href;
          a.target = '_blank';              // 聊天里的外链新开页，不打断会话
          a.rel = 'noopener noreferrer';    // 新页拿不到 window.opener
          a.textContent = m[1];
          parent.appendChild(a);
        } else {   // 协议不在白名单：整链字面渲染（可见、不可点）
          parent.appendChild(document.createTextNode(m[0]));
        }
        text = text.slice(b + m[0].length);
        continue;
      }
      // 反引号分支：未配对则字面到结尾
      if (t > 0) parent.appendChild(document.createTextNode(text.slice(0, t)));
      var j = text.indexOf('`', t + 1);
      if (j < 0) { parent.appendChild(document.createTextNode(text.slice(t))); return; }
      var code = document.createElement('code');
      code.textContent = text.slice(t + 1, j);
      parent.appendChild(code);
      text = text.slice(j + 1);
    }
    if (text) parent.appendChild(document.createTextNode(text));
  }

  // ---- 块级：围栏/标题/列表/纯文本，逐行状态机 ----
  var HEADING_RE = /^(#{1,6}) +(.*)$/;      // # 后必须空格（#5 bolt 是文本）
  var UL_RE = /^[-*+] +(.*)$/;
  var OL_RE = /^\d{1,9}[.)] +(.*)$/;        // 序号不回填 li value——子集口径

  function render(el, text) {
    el.replaceChildren();
    var lines = String(text).split('\n');
    var i = 0, plain = '';
    var flushPlain = function () {
      if (plain) { appendInline(el, plain); plain = ''; }
    };
    while (i < lines.length) {
      var line = lines[i];
      if (line.indexOf('```') === 0) {
        flushPlain();
        var buf = [];
        i++;
        while (i < lines.length && lines[i].indexOf('```') !== 0) { buf.push(lines[i]); i++; }
        i++;  // 越过闭合围栏；EOF 即未闭合——吞到末尾（流式半截定义行为）
        var pre = document.createElement('pre');
        pre.textContent = buf.join('\n');
        el.appendChild(pre);
        continue;
      }
      var h = HEADING_RE.exec(line);
      if (h) {
        flushPlain();
        var hn = document.createElement('h' + h[1].length);
        appendInline(hn, h[2]);
        el.appendChild(hn);
        i++;
        continue;
      }
      var isUL = UL_RE.test(line);
      var isOL = !isUL && OL_RE.test(line);
      if (isUL || isOL) {
        flushPlain();
        var list = document.createElement(isUL ? 'ul' : 'ol');
        var itemRE = isUL ? UL_RE : OL_RE;
        // 条目间空行不终结列表（松散列表——LLM 输出常见「1. a\n\n2. b」，
        // 若空行断列会重头编号）；遇非列表非空行才收口。
        while (i < lines.length) {
          var m2 = itemRE.exec(lines[i]);
          if (m2) {
            var li = document.createElement('li');
            appendInline(li, m2[1]);
            list.appendChild(li);
            i++;
          } else if (lines[i].trim() === '' && i + 1 < lines.length
                     && itemRE.test(lines[i + 1])) {
            i++;  // 列表内空行：跳过（下一行还是列表项）
          } else break;
        }
        el.appendChild(list);
        continue;
      }
      plain += line + (i < lines.length - 1 ? '\n' : '');
      i++;
    }
    flushPlain();
  }

  // 全局出口：页面内联脚本经 window.seekMD.render(el, text) 消费。
  window.seekMD = { render: render };
})();
