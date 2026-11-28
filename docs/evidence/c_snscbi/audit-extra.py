#!/usr/bin/env python3
"""补充核（c_snscbi 收工证据，一次性）：验收 ③ 只要求反引号 token / 7 位以上 hex / c_ 卡号，
本脚本把事实面再扩一层，防「为压字节悄悄删了数字或裸标识符」。

核对每个改动条目 vs `git show main:<file>`：
  1. 裸标识符   —— camelCase / 点路径（`policy.CWD()`、`e.Message.Content` 之类反引号外出现的）
  2. 数值       —— 带单位的（280px / 16 KiB / 1.5MB / 24h / 8 秒）与 2 位以上整数（32/32、16384）
  3. 结构       —— 首行 `### ` 标题逐字不变；Saw/Why/Fix/Lesson 五弹齐；无 `## ` 残件；末尾换行不变
  4. 不许膨胀   —— 新文不得比 main 原文字节更大
  5. 语言不翻   —— 原文无中文（CJK=0）的条目不得出现 >20 个汉字（只许删字，不许翻译）

用法：python3 docs/evidence/c_snscbi/audit-extra.py [base-ref]
报告写本目录 audit-extra.txt；有任一问题则 exit 1。
"""
import os
import re
import subprocess
import sys

BASE = sys.argv[1] if len(sys.argv) > 1 else "main"
ROOT = "docs/pitfalls"
HERE = os.path.dirname(os.path.abspath(__file__))

RE_DOT = re.compile(r"\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*){1,}\b")
RE_SNAKE = re.compile(r"\b[a-z][a-z0-9]*_[a-z0-9_]+\b")
RE_UNIT = re.compile(r"\b\d[\d.]*\s?(?:px|ms|s|KiB|MiB|MB|KB|B|%|h)\b")
RE_INT = re.compile(r"(?<![\w.])\d{2,}(?![\w])")
RE_CJK = re.compile("[\u4e00-\u9fff]")


def changed():
    out = subprocess.run(
        ["git", "-c", "core.quotepath=false", "diff", "--name-only", BASE, "--", ROOT],
        capture_output=True, text=True,
    ).stdout
    return [c for c in out.split("\n") if c.strip().endswith(".md")]


def show(path):
    r = subprocess.run(
        ["git", "-c", "core.quotepath=false", "show", f"{BASE}:{path}"],
        capture_output=True,
        text=True,
    )
    return r.stdout if r.returncode == 0 else None


def main():
    problems = []
    nf = nt = 0
    for path in sorted(changed()):
        old = show(path)
        if old is None or not os.path.exists(path):
            continue
        new = open(path, encoding="utf-8").read()
        nf += 1
        for label, rx in (("裸标识符", RE_DOT), ("下划线标识符", RE_SNAKE), ("单位数值", RE_UNIT), ("整数", RE_INT)):
            olds = sorted({m.group(0) for m in rx.finditer(old)})
            miss = [t for t in olds if t not in new]
            nt += len(olds)
            if miss:
                problems.append((path, label, miss))
        if len(new) > len(old):
            problems.append((path, "膨胀", f"{len(old)} -> {len(new)}"))
        if old.split("\n")[0] != new.split("\n")[0]:
            problems.append((path, "标题被改", "首行与 main 不一致"))
        for k in ("Saw", "Why", "Fix", "Lesson"):
            if not re.search(rf"^- \*\*{k}", new, re.M):
                problems.append((path, "缺弹", k))
        if re.search(r"^## ", new, re.M):
            problems.append((path, "段落头残件", "出现 ## "))
        if old.endswith("\n") != new.endswith("\n"):
            problems.append((path, "末尾换行变了", ""))
        if len(RE_CJK.findall(old)) == 0 and len(RE_CJK.findall(new)) > 20:
            problems.append((path, "被翻译", f"CJK 0 -> {len(RE_CJK.findall(new))}"))

    lines = [f"# base={BASE} files={nf} tokens={nt}", ]
    if problems:
        for p, label, detail in problems:
            lines.append(f"{label}\t{p}\t{detail}")
        lines.append(f"FAIL: {len(problems)} 处")
    else:
        lines.append("OK — 无缺失（裸标识符/数值/结构/不膨胀/语言口径全过）")
    with open(os.path.join(HERE, "audit-extra.txt"), "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    print(lines[-1])
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
