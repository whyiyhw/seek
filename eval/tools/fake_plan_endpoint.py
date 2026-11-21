#!/usr/bin/env python3
"""fake_plan_endpoint.py — scripted OpenAI-compatible model for the
plan-e2e eval case (eval v2 E3, card c_c5wwey).

WHY A SCRIPTED MODEL

`plan-e2e` has to exercise the REAL plan toolchain end to end
(propose → approval → plan(start) → work → plan(complete) → -resume
fold). Unattended eval lanes run fake-key by rule (eval/README §
Isolation), so the *model* cannot be the thing under measurement:
with a fake key the only model available is one this repo scripts. What
the case measures is the TOOLCHAIN — the wire-format markers, the
approval gate, the transcript record, and the resume fold — all of
which are deterministic given a fixed tool-call sequence. Model
*choice* (does a real model call plan() in order?) is out of scope and
is exactly what E3's README says must not be inferred from this case.

State machine (keyed on the transcript the endpoint receives, never on
wall-clock or request counters — a retried request must produce the
same answer):

  fresh lane
    1. no propose result yet                 → propose(...)
    2. last result is the propose result     → plan(start, 1)
    3. plan(start) done, no write yet        → write notes.md
    4. write done, no plan(complete)         → plan(complete, 1)
    5. plan(complete) done                   → text PLAN-E2E-DONE

  resume lane (the follow-up prompt text marks it — see RESUME_MARKER)
    6. no plan(complete, 2) result yet       → plan(complete, 2)
    7. that call settled                     → text RESUME-E2E-DONE

Step 6 is the fold probe: with the approved plan correctly reconstructed
from the transcript, `plan(complete, 2)` answers
"[plan: step 2 complete, 2/2 done]"; with the plan state lost it
answers "no active plan". The case asserts on that tool-result SHAPE,
never on prose.

Usage:
    PORT=18085 LOG=/tmp/req.jsonl python3 eval/tools/fake_plan_endpoint.py
    (then point seek at http://127.0.0.1:18085/v1/chat/completions)

Stdlib only, same posture as eval/tools/fake_openai_compat.py.
"""
import json
import os

from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.environ.get("PORT", "18085"))
LOG = os.environ.get("LOG", "")

# The follow-up prompt the resume run sends. Kept here (and mirrored in
# eval/cases/plan-e2e/runner.sh) so the endpoint can tell the two lanes
# apart without depending on request order.
RESUME_MARKER = "RESUME-FOLD-CHECK"

PROBLEM = "Create notes.md in the fixture workspace and read it back."
DONE_WHEN = [
    "file_check: notes.md exists in the working directory",
    "content_check: notes.md contains the line FIXTURE-NOTES-OK",
]
STEPS = [
    "Create notes.md containing FIXTURE-NOTES-OK",
    "Read notes.md back to confirm its content",
]
REVIEWED_BY = "eval runner re-reads the transcript and the written file"


def _chunks(cid, name, args):
    return [
        {"choices": [{"index": 0, "delta": {"role": "assistant", "tool_calls": [
            {"index": 0, "id": cid, "type": "function",
             "function": {"name": name, "arguments": json.dumps(args)}}]},
            "finish_reason": None}]},
        {"choices": [{"index": 0, "delta": {}, "finish_reason": "tool_calls"}],
         "usage": {"prompt_tokens": 900, "completion_tokens": 30}},
    ]


def _text(txt):
    return [
        {"choices": [{"index": 0, "delta": {"role": "assistant", "content": txt},
                      "finish_reason": None}]},
        {"choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
         "usage": {"prompt_tokens": 900, "completion_tokens": 20}},
    ]


def _tool_msgs(msgs):
    """[(call name, content)] for every tool message, in order.

    The wire never carries a tool name on tool-role messages (only
    tool_call_id), so the name is recovered from the assistant tool_call
    that shares the id — the same pairing the plan reconstructor does.
    """
    by_id = {}
    for m in msgs:
        for tc in (m.get("tool_calls") or []):
            by_id[tc.get("id")] = ((tc.get("function") or {}).get("name") or "")
    out = []
    for m in msgs:
        if m.get("role") == "tool":
            out.append((by_id.get(m.get("tool_call_id"), ""), m.get("content") or ""))
    return out


def decide(msgs):
    tools = _tool_msgs(msgs)
    names = [n for n, _ in tools]
    last_user = ""
    for m in reversed(msgs):
        if m.get("role") == "user":
            last_user = m.get("content") or ""
            break
    resume_lane = RESUME_MARKER in last_user

    if resume_lane:
        # The fold probe. On a lane whose plan state folded correctly,
        # this is a no-op complete on an already-completed step and the
        # result says "2/2 done"; with no folded plan it errors.
        if not any(n == "plan" and "step 2 complete" in c for n, c in tools):
            if not any(n == "plan" and c.startswith("[plan: step 2") for n, c in tools):
                return _chunks("c-fold", "plan", {"action": "complete", "index": 2})
        return _text("RESUME-E2E-DONE")

    if "propose" not in names:
        return _chunks("c-propose", "propose", {
            "problem": PROBLEM,
            "done_when": DONE_WHEN,
            "reviewed_by": REVIEWED_BY,
            "steps": STEPS,
        })
    if "plan" not in names:
        return _chunks("c-start", "plan", {"action": "start", "index": 1})
    if "write" not in names:
        return _chunks("c-write", "write",
                       {"path": "notes.md", "content": "FIXTURE-NOTES-OK\n"})
    if not any(n == "plan" and "complete" in c for n, c in tools):
        return _chunks("c-complete", "plan", {"action": "complete", "index": 1})
    return _text("PLAN-E2E-DONE")


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n)
        try:
            req = json.loads(body or b"{}")
        except ValueError:
            req = {}
        msgs = req.get("messages") or []
        chunks = decide(msgs)
        if LOG:
            with open(LOG, "a") as f:
                f.write(json.dumps({
                    "path": self.path,
                    "model": req.get("model", ""),
                    "n_messages": len(msgs),
                    "decision": chunks[0]["choices"][0]["delta"].get(
                        "tool_calls", [{}])[0].get("function", {}).get("name")
                    if chunks[0]["choices"][0]["delta"].get("tool_calls") else "text",
                }) + "\n")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for c in chunks:
            self.wfile.write(b"data: " + json.dumps(c).encode() + b"\n\n")
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
