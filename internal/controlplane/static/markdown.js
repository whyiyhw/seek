'use strict';
// seek web — 消息正文 markdown 子集渲染器（W5 卡 c_x2au4t，feature-web-
// frontend §13.1 markdown 行；2026-11-22 W5 后返工卡 c_k3pnlw 补齐 GFM
// 表格 / blockquote / 段落 <p> / 列表折行续行）。static/ 拆分的第一个非
// 内联资产：页面仍单 HTML，本文件由 /static/ 同源自供，CSP 的 script-src
// 'self' 直接覆盖它（内联块才要 hash 钉）。
//
// 选型记录（为何手写而非 vendor）：marked ~39KB / markdown-it ~110KB 源字
// 节双双超 ≤25KB 配额（§13.3 按 embed 源字节 1:1 计），且 HTML-string 输
// 出必然要把字符串解析进 DOM——违反 §5.5「模型输出永不进 HTML 解析
// 路径」的既有纪律。手写子集两头都占：线性一趟、无回溯正则，DOM API
// 白名单构造。覆盖：围栏代码块、行内码、标题（#..######）、无序/有序列
// 表（松散列表 + 折行续行折叠）、GFM 表格（含 \| 转义与对齐）、
// blockquote（> 前缀递归剥层）、段落 <p>、链接；其余原样纯文本。
//
// XSS 纪律（转义优先 + 标签白名单）：文本一律 createTextNode/textContent
// ——浏览器侧即为转义，不存在「先拼 HTML 再过滤」的窗口；元素构造仅限
// PRE/CODE/UL/OL/LI/H1-H6/A/BLOCKQUOTE/DIV/TABLE/THEAD/TBODY/TR/TH/TD
// 白名单；链接 href 过协议白名单（http/https/mailto + 相对路径按页面源
// 解析），javascript:/data: 一类整链按字面渲染为文本。表格对齐走 CSSOM
// 赋值（cell.style.textAlign——ContextMeter width 同款先例，CSP style-src
// 只拦样式字符串，拦不到属性赋值）。整页不存在 HTML 字符串解析路径
// （页面与 W2.1 静态断言同款纪律）。
//
// 流式语义（W2.1 合帧渲染的渲染半边）：纯函数、单趟、每帧对累积全文重
// 渲的代价与文本长度线性相关（聊天级几 KB 亚毫秒）；未闭合围栏吞到文本
// 末尾、未配对 ` 或 [ 保持字面——delta 半截文本天然可渲，reply 收口再全
// 量重渲成权威形状。构造行的标记（## / - / | 竖线）在该行完整抵达的同一
// 帧被吃掉——与 c_x2au4t 交付时同款语义，追加式 delta 的 textContent 单
// 调不缩不受影响。
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

  // ---- GFM 表格：| 分格、\| 转义，首尾框架竖线可省 ----
  // 表格成立条件（GFM 硬口径）：表头行含 |，紧随一行分隔行（每格 :?-+:?），
  // 且两行格数一致。缺任一，含 | 的行按普通段落原样出（非表格不吞字）。
  function splitRow(line) {
    var s = line.trim();
    if (s.charAt(0) === '|') s = s.slice(1);
    if (s.slice(-1) === '|' && trailingBarIsFrame(s)) s = s.slice(0, -1);
    var cells = [], cur = '';
    for (var k = 0; k < s.length; k++) {
      if (s.charAt(k) === '\\' && s.charAt(k + 1) === '|') { cur += '|'; k++; }
      else if (s.charAt(k) === '|') { cells.push(cur.trim()); cur = ''; }
      else cur += s.charAt(k);
    }
    cells.push(cur.trim());
    return cells;
  }
  // 尾竖线是框架还是字面：数它前面的连续反斜杠，奇数 = 被转义的字面
  function trailingBarIsFrame(s) {
    var n = 0;
    for (var k = s.length - 2; k >= 0 && s.charAt(k) === '\\'; k--) n++;
    return n % 2 === 0;
  }
  // 分隔行 → 每格对齐（''/left/right/center）；不是分隔行返回 null
  function tableAligns(line) {
    var cells = splitRow(line), aligns = [];
    for (var c = 0; c < cells.length; c++) {
      if (!/^-+:?$/.test(cells[c]) && !/^:-+:?$/.test(cells[c])) return null;
      var t = cells[c];
      aligns.push(t.charAt(0) === ':' && t.slice(-1) === ':' ? 'center'
        : t.slice(-1) === ':' ? 'right' : t.charAt(0) === ':' ? 'left' : '');
    }
    return aligns;
  }
  function tableCell(tag, text, align) {
    var cell = document.createElement(tag);
    if (align) cell.style.textAlign = align;   // CSSOM：见头注
    appendInline(cell, text);
    return cell;
  }

  // ---- 块级：围栏/标题/引用/表格/列表/段落，逐行状态机 ----
  var HEADING_RE = /^(#{1,6}) +(.*)$/;      // # 后必须空格（#5 bolt 是文本）
  var UL_RE = /^[-*+] +(.*)$/;
  var OL_RE = /^\d{1,9}[.)] +(.*)$/;        // 序号不回填 li value——子集口径
  var QUOTE_RE = /^ {0,3}> ?(.*)$/;
  var MAX_QUOTE_DEPTH = 4;                  // 病态嵌套防栈爆——超深后 > 字面到底

  // 行是否开启新块（围栏/标题/引用/表头+分隔行）——列表续行折叠的守卫：
  // 续行不许吞掉下一个块的开头。
  function isBlockStart(lines, i) {
    var s = lines[i];
    if (s.indexOf('```') === 0 || HEADING_RE.test(s) || QUOTE_RE.test(s)) return true;
    if (s.indexOf('|') >= 0 && i + 1 < lines.length) {
      var a = tableAligns(lines[i + 1]);
      if (a && splitRow(s).length === a.length) return true;
    }
    return false;
  }

  function render(el, text, depth) {
    depth = depth || 0;
    el.replaceChildren();
    var lines = String(text).split('\n');
    var i = 0, para = null;
    var flushPara = function () {
      if (para) {
        var p = document.createElement('p');
        appendInline(p, para.join('\n'));
        el.appendChild(p);
        para = null;
      }
    };
    while (i < lines.length) {
      var line = lines[i];
      if (line.indexOf('```') === 0) {
        flushPara();
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
        flushPara();
        var hn = document.createElement('h' + h[1].length);
        appendInline(hn, h[2]);
        el.appendChild(hn);
        i++;
        continue;
      }
      if (QUOTE_RE.test(line)) {
        flushPara();
        var qbuf = [], qm;
        while (i < lines.length && (qm = QUOTE_RE.exec(lines[i]))) {
          qbuf.push(qm[1]);   // 剥一层 > 前缀；嵌套引用由递归免费获得
          i++;
        }
        var bq = document.createElement('blockquote');
        if (depth < MAX_QUOTE_DEPTH) render(bq, qbuf.join('\n'), depth + 1);
        else appendInline(bq, qbuf.join('\n'));
        el.appendChild(bq);
        continue;
      }
      if (line.indexOf('|') >= 0 && i + 1 < lines.length) {
        var aligns = tableAligns(lines[i + 1]);
        var heads = aligns && splitRow(line);
        if (heads && heads.length === aligns.length) {
          flushPara();
          var wrap = document.createElement('div');   // 横滚容器：窄屏不裁切
          wrap.className = 'md-tbl';
          var tbl = document.createElement('table');
          var thead = document.createElement('thead');
          var tr = document.createElement('tr');
          for (var c = 0; c < heads.length; c++) {
            tr.appendChild(tableCell('th', heads[c], aligns[c]));
          }
          thead.appendChild(tr);
          tbl.appendChild(thead);
          var tbody = document.createElement('tbody');
          i += 2;   // 表头 + 分隔行
          while (i < lines.length && lines[i].trim() !== ''
                 && lines[i].indexOf('|') >= 0) {
            var cells = splitRow(lines[i]);
            var tr2 = document.createElement('tr');
            for (var c2 = 0; c2 < heads.length; c2++) {
              tr2.appendChild(tableCell('td', cells[c2] || '', aligns[c2]));
            }
            tbody.appendChild(tr2);
            i++;
          }
          tbl.appendChild(tbody);
          wrap.appendChild(tbl);
          el.appendChild(wrap);
          continue;
        }
      }
      var isUL = UL_RE.test(line);
      var isOL = !isUL && OL_RE.test(line);
      if (isUL || isOL) {
        flushPara();
        var list = document.createElement(isUL ? 'ul' : 'ol');
        var itemRE = isUL ? UL_RE : OL_RE;
        var li = null;
        // 条目间空行不终结列表（松散列表——LLM 输出常见「1. a\n\n2. b」，
        // 若空行断列会重头编号）；条目后的非空非块行折进上一条目
        // （lazy continuation——「- 长项\n  续行」不断列顶格）；遇真正的
        // 新块或空行后的非条目行才收口。
        while (i < lines.length) {
          var m2 = itemRE.exec(lines[i]);
          if (m2) {
            li = document.createElement('li');
            appendInline(li, m2[1]);
            list.appendChild(li);
            i++;
          } else if (lines[i].trim() === '' && i + 1 < lines.length
                     && itemRE.test(lines[i + 1])) {
            i++;  // 列表内空行：跳过（下一行还是列表项）
          } else if (li && lines[i].trim() !== '' && !isBlockStart(lines, i)) {
            li.appendChild(document.createTextNode('\n'));
            appendInline(li, lines[i].replace(/^\s+/, ''));
            i++;
          } else break;
        }
        el.appendChild(list);
        continue;
      }
      if (line.trim() === '') { flushPara(); i++; continue; }   // 空行 = 段界
      if (!para) para = [];
      para.push(line);   // 段内单换行保留（容器 pre-wrap 下仍断行显示）
      i++;
    }
    flushPara();
  }

  // 全局出口：页面内联脚本经 window.seekMD.render(el, text) 消费。
  window.seekMD = { render: render };
})();
