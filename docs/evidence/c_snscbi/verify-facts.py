#!/usr/bin/env python3
"""一次性核验脚本（c_snscbi 收工用，不入库）：被改条目的"事实 token"在新文里是否逐个仍在。

对比基准 = 卡面上的 main（本分支 base 6339f17c，与 main 同一提交）。
抽取三类事实：
  1. 反引号 token      —— `` `...` `` 内的整段原文（含选择器/路径/数值/常量名）
  2. 提交号            —— 独立成词的 7+ 位十六进制串
  3. 卡号              —— c_ 开头的 id
用法：
  python3 out/pitfalls-slim/verify-facts.py [base-ref] [list-file]
  list-file 每行一条路径（可带 `#` 注释行）；省略则检查 git diff 里的全部改动文件。
输出：只列缺失项；全绿时打印 OK 与统计。报告写本目录 facts-report.txt
（给了 list-file 时写 missing-<list basename>.txt）。
"""
import os
import re
import subprocess
import sys

BASE = sys.argv[1] if len(sys.argv) > 1 else "main"
LIST = sys.argv[2] if len(sys.argv) > 2 else None
ROOT = "docs/pitfalls"
OUT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)))

RE_BACKTICK = re.compile(r"`([^`\n]*)`")
RE_HEX = re.compile(r"\b[0-9a-f]{7,40}\b")
RE_CARD = re.compile(r"\bc_[a-z0-9]{5,}")


def changed_files():
    if LIST:
        with open(LIST, encoding="utf-8") as f:
            return [ln.strip() for ln in f if ln.strip() and not ln.startswith("#")]
    out = subprocess.run(
        ["git", "-c", "core.quotepath=false", "diff", "--name-only", BASE, "--", ROOT],
        capture_output=True, text=True,
    ).stdout
    return [c for c in out.split("\n") if c.strip() and c.endswith(".md")]


def show(path):
    out = subprocess.run(
        ["git", "-c", "core.quotepath=false", "show", f"{BASE}:{path}"],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        return None
    return out.stdout


def code_spans(text):
    """按 CommonMark 规则切行内代码 span：一段反引号开启，必须由**等长**的反引号段关闭。

    这样 `` ` ```json\n[...]\n``` ` `` 这种内部含三连反引号的 span 不会被配对错位，
    而围栏（奇数个 ``` 的行）也不会凭空造出伪 token。
    """
    spans = []
    i = 0
    n = len(text)
    while i < n:
        if text[i] != "`":
            i += 1
            continue
        run = 1
        while i + run < n and text[i + run] == "`":
            run += 1
        start = i + run
        j = start
        while j < n:
            if text[j] == "`":
                r = 1
                while j + r < n and text[j + r] == "`":
                    r += 1
                if r == run:
                    spans.append(text[start:j])
                    i = j + r
                    break
                j += r
            else:
                j += 1
        else:
            i += run
    return spans


def facts(text):
    toks = set()
    for raw in code_spans(text):
        tok = raw.strip()
        if tok:
            toks.add(tok)
    for m in RE_HEX.finditer(text):
        toks.add(m.group(0))
    for m in RE_CARD.finditer(text):
        toks.add(m.group(0))
    return toks


def check_struct(text):
    problems = []
    first = next((ln for ln in text.splitlines() if ln.strip()), "")
    if not first.startswith("### "):
        problems.append("首行不是 `### ` 标题")
    for k in ("Saw", "Why", "Fix", "Lesson"):
        if not re.search(rf"^- \*\*{k}", text, re.M):
            problems.append(f"缺 - **{k}")
    if re.search(r"^## ", text, re.M):
        problems.append("正文出现 `## ` 段落头")
    return problems


def main():
    changed = sorted(c for c in changed_files() if c.endswith(".md"))
    missing_report = []
    n_files = n_tok = 0
    for path in changed:
        if not os.path.exists(path):
            missing_report.append(f"## {path}: 文件已不存在")
            continue
        old = show(path)
        if old is None:
            missing_report.append(f"## {path}: 基线里没有此文件（新增条目，跳过）")
            continue
        new = open(path, encoding="utf-8").read()
        n_files += 1
        olds = facts(old)
        n_tok += len(olds)
        miss = sorted(t for t in olds if t not in new)
        struct = check_struct(new)
        if miss or struct:
            missing_report.append(f"## {path}")
            for m in miss:
                missing_report.append(f"  - 缺事实: {m}")
            for s in struct:
                missing_report.append(f"  - 结构: {s}")

    os.makedirs(OUT_DIR, exist_ok=True)
    name = "facts-report.txt"
    if LIST:
        name = "missing-" + os.path.basename(LIST).replace(".txt", "") + ".txt"
    with open(os.path.join(OUT_DIR, name), "w", encoding="utf-8") as f:
        f.write(f"# base={BASE} list={LIST or 'git-diff'} checked={n_files} facts={n_tok}\n")
        f.write("\n".join(missing_report) + "\n" if missing_report else "OK — 无缺失\n")

    if missing_report:
        print("\n".join(missing_report))
        print(f"\nFAIL: {len([r for r in missing_report if r.startswith('## ')])} 个文件有问题")
        return 1
    print(f"OK: {n_files} 个改动文件、{n_tok} 个事实 token 全部仍在；结构与五弹齐全")
    return 0


if __name__ == "__main__":
    sys.exit(main())
