#!/usr/bin/env python3
"""fake_openai_compat.py — hermetic OpenAI-compatible endpoint for the
eval provider lane (c_ffzic2) and any case that needs to prove WHERE a
request went without a real key or a real provider.

Speaks just enough of the OpenAI Chat-Completions SSE wire (one text
delta + a final usage chunk + [DONE] — same shape fake_paas_glm.py
established) to drive one clean turn of the real seek binary:

    python3 eval/tools/fake_openai_compat.py              # port 18081
    PORT=18123 LOG=/tmp/reqs.jsonl python3 fake_openai_compat.py

Every POST is recorded to $LOG as one JSON line

    {"path": "/v1/chat/completions", "auth": "Bearer fake",
     "model": "deepseek-v4.1-flash", "seek_ua": true}

so tests can assert the request TARGET switched (path), the credential
switched (auth — e.g. the metered-key canary never appears), and the
model knob reached the wire (model). seek_ua records whether the
User-Agent identified as seek (the OpenCode Go gateway's documented
degradation path is a generic UA).

Point the eval provider lane at it (fake key, hermetic — never a real
credential):

    SEEK_EVAL_PROVIDER=opencode \\
    SEEK_EVAL_BASE_URL=http://127.0.0.1:18081/v1/chat/completions \\
    SEEK_EVAL_API_KEY=fake \\
    SEEK_EVAL_RESULTS_DIR=/tmp/eval-out \\
    eval/run.sh <case> ./seek

The provider-lane contract (including the never-refresh-token rule:
this server and its callers must never touch ~/.seek/opencode-auth.json
— refresh tokens are single-use, a side refresh invalidates the copy
seek holds) is documented in eval/README.md § "Provider lane".

Stdlib only (http.server); env-steered so one file serves all shapes.
"""
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.environ.get("PORT", "18081"))
LOG = os.environ.get("LOG", "")

CHUNKS = [
    '{"id":"chatcmpl-fake","choices":[{"index":0,"delta":{"role":"assistant","content":"LANE OK"},"finish_reason":null}]}',
    '{"id":"chatcmpl-fake","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],'
    + '"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0}}}',
    "[DONE]",
]


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n)
        if LOG:
            try:
                req = json.loads(body)
                model = req.get("model", "")
            except Exception:
                model = ""
            rec = {
                "path": self.path,
                "auth": self.headers.get("Authorization", ""),
                "model": model,
                "seek_ua": "seek" in (self.headers.get("User-Agent", "") or "").lower(),
            }
            with open(LOG, "a") as f:
                f.write(json.dumps(rec) + "\n")
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for c in CHUNKS:
            self.wfile.write(b"data: " + c.encode() + b"\n\n")
        self.wfile.flush()

    def log_message(self, *a):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
