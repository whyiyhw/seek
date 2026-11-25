#!/usr/bin/env python3
"""compact-ab-endpoint.py — scripted model for eval/cases/compact-ab.

WHY A SCRIPTED MODEL (and why no judge.py)

The A/B asks one question: **does the compacted history still carry the
facts the task needs?** On the fake-key lane (rule: unattended lanes run
fake-key, eval/README § Isolation) the "model" is always something this
repo scripts, so an LLM judge would be grading a fixture's canned prose
— it measures the endpoint, not compaction. What we CAN measure
deterministically is the thing compaction actually destroys: whether the
three golden facts survive into the request the model receives.

So this endpoint does extractive answering over the request body:

  - all three FACTS present in the concatenated message content
        → emit the facts (an answer that could satisfy the rubric)
  - otherwise
        → emit NO-FACTS-AVAILABLE (an answer that provably cannot)

That makes the case mechanical and zero-cost, and it makes the negative
arm (a summary that dropped the facts) turn red for the right reason.
docs/prd/feature-eval-v3.md's judge axis stays untouched: a real-key
lane can add a `judge` block later; the rubric text is already encoded
here as FACTS.

Usage:
    PORT=18086 python3 eval/tools/compact-ab-endpoint.py
Stdlib only, same posture as eval/tools/fake_openai_compat.py.
"""
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.environ.get("PORT", "18086"))

FACTS = ["internal/csvexport/export.go", "report.csv", "45s"]
NO_FACTS = "NO-FACTS-AVAILABLE"

# ALWAYS_ANSWER=1 makes the endpoint answer the golden facts unconditionally
# — the sabotage knob `scripts/eval-compact-ab-test.sh` drives to prove the
# bad-variant assertion actually bites (a scorer that cannot fail is not a
# scorer). Never set in a real run.
ALWAYS_ANSWER = bool(os.environ.get("ALWAYS_ANSWER", ""))


def _body_text(req):
    out = []
    for m in req.get("messages") or []:
        c = m.get("content")
        if isinstance(c, str):
            out.append(c)
        elif isinstance(c, list):
            for part in c:
                if isinstance(part, dict) and isinstance(part.get("text"), str):
                    out.append(part["text"])
    return "\n".join(out)


def answer_for(req):
    body = _body_text(req)
    found = [f for f in FACTS if f in body]
    if len(found) == len(FACTS) or ALWAYS_ANSWER:
        return ("The exporter is internal/csvexport/export.go, it writes "
                "report.csv, and the flush timeout is 45s.")
    # The negative answer must NOT echo the fact strings: an extractive
    # scorer looks for them in the answer text, so "NO-FACTS-AVAILABLE
    # (missing: internal/csvexport/export.go, …)" would read as a
    # successful recovery. Report the count only.
    return "%s (%d/%d facts in history)" % (NO_FACTS, len(found), len(FACTS))


def _chunk(text):
    return json.dumps({
        "id": "chatcmpl-compact-ab",
        "choices": [{"index": 0,
                     "delta": {"role": "assistant", "content": text},
                     "finish_reason": None}],
    })


def _final(prompt_tokens, completion_tokens):
    return json.dumps({
        "id": "chatcmpl-compact-ab",
        "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        "usage": {"prompt_tokens": prompt_tokens,
                  "completion_tokens": completion_tokens,
                  "total_tokens": prompt_tokens + completion_tokens},
    })


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(n)
        try:
            req = json.loads(raw or b"{}")
        except ValueError:
            req = {}
        # The runner's readiness probe sends a message-less POST; answering
        # it without touching FACTS keeps the probe from looking like a
        # successful arm (its text is never asserted on either way).
        text = answer_for(req) if req.get("messages") else "ping-authorized"
        chunks = [_chunk(text),
                  # prompt_tokens is deliberately the CONCATENATED history
                  # size proxy: run.sh reports it, a future bound can use it
                  # to show arm B's request is genuinely shorter.
                  _final(prompt_tokens=len(_body_text(req)) or 1,
                         completion_tokens=len(text)),
                  "[DONE]"]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for c in chunks:
            self.wfile.write(b"data: " + c.encode() + b"\n\n")
        self.wfile.flush()

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
