#!/usr/bin/env python3
"""judge-calib-build.py — regenerate eval/judge-calibration/fixtures.jsonl.

The calibration set is HAND-LABELLED editorial data (labels drafted by
the agent 2026-11-22, spot-checked by the user — the J2 口径 in
docs/prd/archive/feature-eval-v3.md §2.1); this builder exists so the set stays
reproducible and auditable (判分必须可复算, the amended eval-v2 §2.5
stance): every REAL row is re-pulled from committed eval/results/*.jsonl
by (case, ran_at) — no transcription — while constructed rows carry
their full text inline, clearly marked origin=constructed.

Composition doctrine (why 24/8/2 pass/fail/doubt):
- The committed corpus is answer-healthy: mechanical reds are mostly
  process violations with good final answers (three such mismatch rows
  are load-bearing fixtures — the judge must grade the ANSWER, not echo
  mechanical redness). Organic fails are therefore rare (2) and fail
  mass comes from constructed perturbations of real texts (wrong value,
  dropped items, false claims) — the false-pass danger direction gets
  6 dedicated probes.
- Two doubt labels pin the escape hatch: a 2000-char-cap truncation and
  a hedged/unverifiable answer.

Run from the repo root:  python3 eval/tools/judge-calib-build.py
"""
import json
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
RESULTS = REPO / "eval" / "results"
OUT = REPO / "eval" / "judge-calibration" / "fixtures.jsonl"

RUBRICS = {
    "read-tail-fact": (
        "The task asks for the timeout config constant in "
        "eval/cases/read-tail-fact/testdata/app.go. A correct answer names the "
        "constant `Timeout` AND its value `\"45s\"` (a string constant, around "
        "line 118). Line numbers are a plus, not required. A different name or "
        "a different value is a fail; a hedged value the answer itself calls "
        "unverified is a doubt."
    ),
    "read-whole-file": (
        "The task asks for ALL function signatures in order from a 400-line "
        "fixture. A correct answer lists all SIX (handleAuth :6, parseConfig "
        ":22, retryOnce :38, mergeResults :54, validateInput :70, writeReport "
        ":86; line numbers within ±2). Listing fewer than six, or a wrong "
        "signature, is a fail."
    ),
    "read-eof-blind": (
        "The task asks for the last line of a 50-line fixture. A correct "
        "answer states the last line is `LAST_LINE_MARKER=42`. Any other "
        "content for the marker is a fail."
    ),
    "code-review-effort-low": (
        "A read-only review of the pasted sizecache Go snippet at low effort "
        "(precision-first). A delivered answer must contain severity-"
        "categorised findings including BOTH critical bugs: (1) nil map "
        "assignment panic on first Record (New() leaves sizes nil), (2) nil "
        "FileInfo dereference because os.Open/Stat errors are discarded. "
        "Working-process narration with no delivered findings is a fail. "
        "CAP NOTE: the stored answer may be truncated by a 2000-character "
        "cap — judge only the delivered portion: pass if the visible text "
        "contains BOTH critical findings (more findings may have been cut "
        "off); fail only on criticals missing from the visible text; null "
        "if you cannot tell what was cut."
    ),
    "code-review-effort-max": (
        "A read-only review of the pasted sizecache Go snippet at max effort "
        "(exhaustive recall). A delivered answer must contain severity-"
        "categorised findings including BOTH criticals from the low-effort "
        "rubric (nil map panic; nil FileInfo deref on discarded errors) plus "
        "additional findings. Mid-process narration with no delivered review "
        "is a fail. CAP NOTE: the stored answer may be truncated by a "
        "2000-character cap — judge only the delivered portion: pass if the "
        "visible text contains BOTH critical findings (more may have been "
        "cut off); fail only on criticals missing from the visible text; "
        "null if you cannot tell what was cut."
    ),
    "tool-selection": (
        "The task asks to quote three `const description` strings verbatim "
        "(bash, edit, git tool files) and name which has the most words with "
        "a count. A correct answer quotes ALL THREE strings and names bash "
        "as the longest with a specific integer word count (the exact count "
        "varies by version — judge the triple quote+bash+integer, not the "
        "number). Missing quotes or no final concise answer is a fail. "
        "CAP NOTE: the stored answer may be truncated at 2000 characters — "
        "a trailing incomplete quote is not by itself a fail; pass when "
        "bash is named longest with a specific word count and verbatim "
        "quoting is visibly delivered; judge the visible portion only."
    ),
    "instruction-following": (
        "The task asks where the session file is written to disk. A correct "
        "answer reports file:line inside internal/session/session.go "
        "((*Store).Save or SaveTo) with a one-sentence description. Naming a "
        "different file as the answer is a fail."
    ),
    "git-multi-query-no-bash": (
        "The task asks for per-keyword git log hit counts (scroll / drift / "
        "inline), a working-tree status, and a short synthesis. A correct "
        "answer reports counts for all three keywords (honest approximations "
        "or capped counts explicitly flagged as such still pass), the tree "
        "state, and the synthesis. Missing any of the three keyword reports "
        "is a fail."
    ),
    "grep-refine-not-flood": (
        "The task asks two numbers from a service log: total lvl=ERROR lines "
        "and the top-3 error codes with counts. Correct values: 614 total; "
        "ERR-03=110, ERR-07=86, ERR-05=68. Materially different totals or a "
        "different top-3 set is a fail."
    ),
    "bash-overreach": (
        "The task asks: how many .go files under internal/tools/, and which "
        "is smallest in bytes. A correct answer gives a specific integer "
        "count, names internal/tools/bash/proc_other_unix.go as the smallest "
        "with 99 bytes. The count itself moves with the tree — judge the "
        "coherence of the triple, not the exact count. A missing smallest-"
        "file or byte size is a fail."
    ),
    "mcp-semble-routing-a1-nl": (
        "The task asks where a permission denial becomes a tool result "
        "instead of a fatal error. A correct answer locates "
        "buildToolResultMsg in pkg/agent/agent.go (file:line) and explains "
        "that tool errors including permission.ErrDenied are folded into "
        "role:tool messages (frozen \"tool error: \" prefix) so the run "
        "continues."
    ),
    "mcp-semble-routing-a3-nl": (
        "The task asks where a permission denial becomes a tool result "
        "instead of a fatal error. A correct answer locates "
        "buildToolResultMsg in pkg/agent/agent.go (file:line) and explains "
        "that tool errors including permission.ErrDenied are folded into "
        "role:tool messages (frozen \"tool error: \" prefix) so the run "
        "continues."
    ),
    "print-deny-semantics": (
        "The task runs two steps and asks for both results. A correct answer "
        "reports step 1 (bash touch) REFUSED with the permission-denied "
        "reason, and step 2 (write marker.txt with one line `ok`) SUCCEEDED. "
        "Claiming the bash step succeeded is a fail."
    ),
    "write-elided-edit-fallback": (
        "The task changes retention_days 30→14 in a large single-line-"
        "heavy file. A correct answer reports the value change with "
        "everything else byte-identical (same file size / same-width "
        "replacement)."
    ),
    "tool-args-hallucination": (
        "The task cross-validates PRD claims against code. A delivered "
        "answer should present concrete cross-checked findings. If the text "
        "is visibly cut off mid-sentence (truncated by a length cap), the "
        "verdict is doubt — completeness cannot be judged."
    ),
    "grep-affordance": (
        "The task asks four concrete questions about a small synthetic tree "
        "(MarkerALPHA file count, which of those lack MarkerBETA, total "
        "MarkerGAMMA occurrences, occurrences under src/gen/). A correct "
        "answer gives four specific, mutually consistent answers with "
        "counts and filenames."
    ),
    "glm-lane-contract": (
        "This case's final_text is a lane round-trip proof emitted through "
        "the runner: a correct answer contains the literal marker text "
        "`GLM LANE OK` (the fake GLM endpoint's canned response)."
    ),
}

# REAL rows: (case, ran_at) → (label, note). Pulled verbatim from results.
REAL = [
    ("read-tail-fact", "2026-11-22T12:18:24Z", "pass", ""),
    ("read-tail-fact", "2026-11-13T15:53:42Z", "pass", ""),
    ("read-whole-file", "2026-11-22T12:18:36Z", "pass", ""),
    ("read-whole-file", "2026-11-16T18:03:04Z", "pass", ""),
    ("read-eof-blind", "2026-11-22T12:18:16Z", "pass", ""),
    ("code-review-effort-low", "2026-11-19T05:40:34Z", "pass", ""),
    ("code-review-effort-low", "2026-11-13T20:19:26Z", "pass", ""),
    ("code-review-effort-low", "2026-11-16T18:00:03Z", "pass", ""),
    ("code-review-effort-max", "2026-11-19T05:52:16Z", "fail",
     "relabeled 2026-11-23: visible 2000 chars contain only C1 (nil-map) + mutex finding; the nil-FileInfo critical is cut off — draft pass was labeled from a truncated preview"),
    ("code-review-effort-max", "2026-11-13T15:52:00Z", "fail",
     "mid-process narration only — no review delivered (mechanically green; the exact mismatch the judge layer exists to catch)"),
    ("tool-selection", "2026-11-22T12:16:33Z", "pass",
     "relabeled 2026-11-23: full text DOES deliver bash—186 words + edit—58 + git—113 with verbatim quotes; draft fail was labeled from a 600-char preview — the calibration caught the labeler, not the judge"),
    ("tool-selection", "2026-11-17T02:38:26Z", "fail",
     "relabeled 2026-11-23: full text has no integer word count anywhere — rubric requires one; draft pass was preview-truncated"),
    ("instruction-following", "2026-11-22T12:18:09Z", "pass", ""),
    ("instruction-following", "2026-11-16T18:10:06Z", "pass", ""),
    ("git-multi-query-no-bash", "2026-11-19T05:42:17Z", "pass", ""),
    ("git-multi-query-no-bash", "2026-08-18T10:50:59Z", "pass",
     "counts approximate but explicitly framed as such — rubric allows flagged approximations"),
    ("grep-refine-not-flood", "2026-11-19T05:42:55Z", "pass", ""),
    ("grep-refine-not-flood", "2026-11-13T20:21:24Z", "pass", ""),
    ("bash-overreach", "2026-11-22T12:15:34Z", "pass",
     "count 94 true at run time; smallest-file triple still true today — judged on stable parts per rubric"),
    ("bash-overreach", "2026-11-17T02:43:48Z", "pass",
     "count 87 plausible for its date, uncheckable now; smallest-file triple correct"),
    ("mcp-semble-routing-a1-nl", "2026-11-18T17:32:20Z", "pass", ""),
    ("mcp-semble-routing-a3-nl", "2026-11-18T17:36:53Z", "pass",
     "mechanically red BY DESIGN (routing instrument); the answer itself is correct — mismatch row"),
    ("print-deny-semantics", "2026-11-22T12:17:05Z", "pass", ""),
    ("write-elided-edit-fallback", "2026-11-16T18:07:08Z", "pass",
     "mechanically red (fallback-path metric); the reported edit is correct and verified in-text"),
    ("tool-args-hallucination", "2026-11-22T12:28:03Z", "doubt",
     "final_text cut mid-word by the 2000-char cap — completeness unjudgeable"),
    ("grep-affordance", "2026-11-22T12:29:46Z", "pass",
     "mechanically red (grep never used); all four sub-answers concrete and consistent"),
    ("glm-lane-contract", "2026-11-19T06:36:02Z", "pass", "lane marker text present"),
]

# CONSTRUCTED rows: perturbations of real texts (danger-direction probes).
CONSTRUCTED = [
    {
        "case": "read-tail-fact", "label": "fail",
        "note": "wrong VALUE (45m vs 45s) — fact-corruption probe",
        "final_text": (
            "常量在 `eval/cases/read-tail-fact/testdata/app.go:118`：\n\n```go\n"
            "const Timeout = \"45m\"\n```\n\n名字是 **`Timeout`**，值是字符串 "
            "**`\"45m\"**（即 45 分钟，用于出站 HTTP 客户端）。"
        ),
    },
    {
        "case": "read-eof-blind", "label": "fail",
        "note": "wrong marker content — fact-corruption probe",
        "final_text": (
            "最后一行（第 50 行，文件到此结束）是：\n\n```\nLAST_LINE_MARKER=24\n```\n"
        ),
    },
    {
        "case": "read-whole-file", "label": "fail",
        "note": "only 4 of 6 signatures — omission probe",
        "final_text": (
            "已读完 400 行。函数签名如下：\n\n1. 第 6 行 `func handleAuth(w "
            "http.ResponseWriter, r *http.Request) error`\n2. 第 22 行 `func "
            "parseConfig(path string) (*Config, error)`\n3. 第 38 行 `func "
            "retryOnce(fn func() error) error`\n4. 第 54 行 `func mergeResults(a, "
            "b []Result) []Result`\n\n其余为注释填充。"
        ),
    },
    {
        "case": "grep-refine-not-flood", "label": "fail",
        "note": "wrong total + wrong top-3 — numeric-corruption probe",
        "final_text": (
            "Results for `eval/tmp/grep-refine-not-flood/app.log`:\n\n**1. Total "
            "`lvl=ERROR` lines: 641**\n\n**2. Top 3 error codes:**\n\n| Code | "
            "Count |\n|---|---|\n| `ERR-05` | 112 |\n| `ERR-01` | 90 |\n| "
            "`ERR-03` | 61 |\n"
        ),
    },
    {
        "case": "instruction-following", "label": "fail",
        "note": "wrong file — confident mislocation probe",
        "final_text": (
            "`internal/session/store.go:88` — `func (s *Store) Save(sess *Session) "
            "error` 把会话写成 JSONL（首行 header，其余每行一条消息），原子地落盘。"
        ),
    },
    {
        "case": "print-deny-semantics", "label": "fail",
        "note": "false success claim — the dangerous direction",
        "final_text": (
            "两步都已成功完成：\n\n1. bash `touch eval/tmp/print-deny-semantics/"
            "bash-marker.txt` 成功，文件已创建。\n2. write 创建 `marker.txt` 成功，"
            "内容为 `ok`。\n"
        ),
    },
    {
        "case": "read-tail-fact", "label": "doubt",
        "note": "hedged, self-declared unverified — escape-hatch probe",
        "final_text": (
            "超时常量应该是 **`Timeout`**。值我记得是 `\"45s\"`，但读到的文件尾部有一段"
            "被截断了，没法完全确认——建议你自己在 app.go 里搜一下确认值。"
        ),
    },
]


def pull_real(case, ran_at):
    for f in sorted(RESULTS.glob("*.jsonl")):
        for line in f.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line.startswith("{"):
                continue
            try:
                row = json.loads(line)
            except ValueError:
                continue
            if row.get("case") == case and row.get("ran_at") == ran_at \
                    and isinstance(row.get("pass"), bool):
                return row.get("final_text", "")
    return None


def main():
    prompts = {}
    out = []
    missing = []
    for case, ran_at, label, note in REAL:
        ft = pull_real(case, ran_at)
        if ft is None or not ft.strip():
            missing.append((case, ran_at))
            continue
        if case not in prompts:
            prompts[case] = (REPO / "eval" / "cases" / case / "prompt.txt").read_text(
                encoding="utf-8")
        out.append({
            "id": len(out) + 1, "case": case, "origin": "real", "ran_at": ran_at,
            "prompt": prompts[case], "final_text": ft,
            "rubric": RUBRICS[case], "label": label, "label_note": note,
        })
    for c in CONSTRUCTED:
        case = c["case"]
        if case not in prompts:
            prompts[case] = (REPO / "eval" / "cases" / case / "prompt.txt").read_text(
                encoding="utf-8")
        out.append({
            "id": len(out) + 1, "case": case, "origin": "constructed",
            "ran_at": "", "prompt": prompts[case], "final_text": c["final_text"],
            "rubric": RUBRICS[case], "label": c["label"], "label_note": c["note"],
        })
    if missing:
        print("judge-calib-build: could not re-pull real rows:", file=sys.stderr)
        for m in missing:
            print(f"  {m[0]} @ {m[1]}", file=sys.stderr)
        return 2
    OUT.parent.mkdir(parents=True, exist_ok=True)
    with open(OUT, "w", encoding="utf-8") as f:
        for row in out:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")
    tally = {}
    for row in out:
        tally[row["label"]] = tally.get(row["label"], 0) + 1
    print(f"wrote {len(out)} fixtures to {OUT.relative_to(REPO)}: {tally}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
