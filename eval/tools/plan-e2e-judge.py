#!/usr/bin/env python3
"""plan-e2e-judge.py — mechanical scorer for the plan-e2e eval case
(eval v2 E3, card c_c5wwey).

Reads the artifacts a run leaves behind and emits one runner_fact line
per assertion; the eval gate (eval/run.sh) counts `failed: true` lines
via the runner_assert_failures metric. Zero LLM judging, zero prose
matching: every assertion is either a wire-format PREFIX check on a
tool result (the markers in internal/tools/plan/plan.go and
propose.go are the contract), a message-order fact from the session
JSONL, or a filesystem fact from the fixture workspace.

INPUT (all required):
  --fresh <session.jsonl>   the session file (fresh lane then resume
                            lane appended — both write the same file)
  --fresh-lines N           line count after the fresh lane; lines
                            beyond it are the resume segment
  --ws DIR                  fixture workspace (notes.md lands here)
  --status1 FILE            drive status JSON, fresh lane
  --status2 FILE            drive status JSON, resume lane

OUTPUT: one {"type":"runner_fact","assert":...,"failed":...,"detail":...}
line per assertion on stdout. Exit code = number of failed assertions
(capped at 60) so the nonzero_exits metric sees a break too.

Why the tool-name pairing exists here too: persisted tool messages
carry no `name` field (Message.Name is omitempty; the name lives in
the assistant tool_call) — the same fact plan.ReconstructFromTranscript
trips over (see its toolNameAt). This judge deliberately re-derives
names the same way so it scores the same transcript the product reads.
"""
import json
import sys

APPROVED = "[plan: approved]"
WRITES = ("write", "edit")


def parse(argv):
    opts = {}
    i = 0
    while i < len(argv):
        a = argv[i]
        if a in ("--fresh", "--fresh-lines", "--ws", "--status1", "--status2"):
            opts[a[2:]] = argv[i + 1]
            i += 2
            continue
        sys.stderr.write("plan-e2e-judge: bad arg %r\n" % a)
        sys.exit(2)
    missing = [k for k in ("fresh", "fresh-lines", "ws", "status1", "status2") if k not in opts]
    if missing:
        sys.stderr.write("plan-e2e-judge: missing %s\n" % ", ".join(missing))
        sys.exit(2)
    return opts


def load_msgs(path):
    msgs = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                msgs.append(json.loads(line))
    return msgs


def call_name(msgs, i):
    """Tool name for the tool-role message at i, via the assistant
    tool_call pairing (persisted tool rows have no name field)."""
    m = msgs[i]
    if m.get("name"):
        return m["name"]
    tid = m.get("tool_call_id", "")
    for j in range(i - 1, -1, -1):
        tcs = msgs[j].get("tool_calls") if isinstance(msgs[j], dict) else None
        if not isinstance(tcs, list):
            continue
        for tc in tcs:
            if tc.get("id") == tid:
                return (tc.get("function") or {}).get("name", "")
    return ""


def tool_calls(msgs):
    """[(line_index, name, args_dict)] for every assistant tool call,
    in transcript order. Guards the header row: line 0 carries session
    metadata where `tool_calls` is a COUNT (int), not a list."""
    out = []
    for i, m in enumerate(msgs):
        if not isinstance(m, dict) or m.get("role") != "assistant":
            continue
        tcs = m.get("tool_calls")
        if not isinstance(tcs, list):
            continue
        for tc in tcs:
            fn = tc.get("function") or {}
            try:
                args = json.loads(fn.get("arguments") or "{}")
            except ValueError:
                args = None
            out.append((i, fn.get("name", ""), args))
    return out


def tool_results(msgs):
    """[(line_index, name, content)] for every tool result message."""
    out = []
    for i, m in enumerate(msgs):
        if isinstance(m, dict) and m.get("role") == "tool":
            out.append((i, call_name(msgs, i), m.get("content") or ""))
    return out


def main():
    opts = parse(sys.argv[1:])
    msgs = load_msgs(opts["fresh"])
    fresh_lines = int(opts["fresh-lines"])
    s1 = json.load(open(opts["status1"]))
    s2 = json.load(open(opts["status2"]))
    results = []

    def check(aid, ok, detail):
        results.append((aid, bool(ok), detail))

    calls = tool_calls(msgs)
    res = tool_results(msgs)

    # A1 propose carries the assertion list: done_when >= 2 entries,
    # reviewed_by non-empty, steps >= 2. This is the propose→done_when
    # half of the chain: a plan without completion conditions cannot
    # be closed by anyone.
    propose_calls = [c for c in calls if c[1] == "propose"]
    pc = propose_calls[0] if propose_calls else None
    if pc is None:
        check("A1_propose_done_when", False, "no propose tool call in transcript")
    else:
        args = pc[2] or {}
        dw = args.get("done_when") or []
        check("A1_propose_done_when",
              len(dw) >= 2 and bool(args.get("reviewed_by")) and len(args.get("steps") or []) >= 2,
              "done_when=%d reviewed_by=%r steps=%d" % (len(dw), args.get("reviewed_by", ""), len(args.get("steps") or [])))

    # A2 the approval marker — wire-format prefix, never prose.
    approvals = [(i, c) for (i, n, c) in res if n == "propose" and c.startswith(APPROVED)]
    check("A2_approval_marker", bool(approvals), "%d propose results start with %r" % (len(approvals), APPROVED))

    # A3 zero write/edit BEFORE the approval line (plan-analyze is
    # read-only; the scripted model must not have smuggled a write).
    ap_line = approvals[0][0] if approvals else len(msgs)
    writes_before = [c for c in calls if c[1] in WRITES and c[0] < ap_line]
    check("A3_no_write_before_approval", not writes_before,
          "%d write/edit calls before approval line %d" % (len(writes_before), ap_line))

    # A4 execution-period echo shapes (the plan tool's wire format).
    started = any(c.startswith("[plan: step 1 started]") for (_, _, c) in res)
    done1 = any(c.startswith("[plan: step 1 complete, 1/2 done]") for (_, _, c) in res)
    check("A4_plan_echo_shapes", started and done1,
          "step1_started=%s step1_complete_1of2=%s" % (started, done1))

    # A5/A6 done_when recomputed against the filesystem — the
    # completion-comparison half of the chain. The two entries mirror
    # the done_when list the fixture proposes (fake_plan_endpoint.py).
    import os
    notes = os.path.join(opts["ws"], "notes.md")
    exists = os.path.isfile(notes)
    content_ok = False
    if exists:
        content_ok = "FIXTURE-NOTES-OK" in open(notes, encoding="utf-8", errors="replace").read()
    check("A5_done_when_file_check", exists, notes)
    check("A6_done_when_content_check", content_ok, notes if exists else "n/a")

    # A7 the resume fold probe: plan(complete, 2) on the resumed
    # session answers "2/2 done" only when the approved plan state was
    # reconstructed from the transcript (plan/reconstruct.go). A lost
    # fold answers "no active plan" — a different SHAPE, not prose.
    resume_res = [(i, n, c) for (i, n, c) in res if i + 1 > fresh_lines]
    fold_ok = any(c.startswith("[plan: step 2 complete, 2/2 done]") for (_, _, c) in resume_res)
    fold_err = any("no active plan" in c for (_, _, c) in resume_res)
    check("A7_resume_fold", fold_ok and not fold_err,
          "fold_probe_ok=%s no_active_plan_errors=%s" % (fold_ok, fold_err))

    # A8 resume must not re-run completed steps: no write/edit in the
    # resume segment (the fixture's step 1 wrote notes.md already).
    resume_writes = [c for c in calls if c[1] in WRITES and c[0] + 1 > fresh_lines]
    check("A8_resume_no_rewrite", not resume_writes,
          "%d write/edit calls in resume segment" % len(resume_writes))

    # A9/A10 driver-level facts: the TUI really typed, approved, saw
    # the done markers and exited cleanly in both lanes.
    check("A9_drive_fresh", all(s1.get(k) for k in ("prompt_sent", "approved", "done", "exited")),
          json.dumps(s1))
    check("A10_drive_resume", all(s2.get(k) for k in ("prompt_sent", "done", "exited")),
          json.dumps(s2))

    failed = 0
    for aid, ok, detail in results:
        if not ok:
            failed += 1
        print(json.dumps({"type": "runner_fact", "assert": aid,
                          "failed": not ok, "detail": detail}))
    sys.stderr.write("plan-e2e-judge: %d/%d assertions passed\n" % (len(results) - failed, len(results)))
    return min(failed, 60)


if __name__ == "__main__":
    sys.exit(main())
