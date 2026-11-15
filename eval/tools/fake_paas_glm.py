#!/usr/bin/env python3
"""fake_paas_glm.py — hermetic shakedown server for eval/cases/glm-lane-contract.

Speaks just enough of bigmodel.cn's paas/v4 chat/completions SSE wire
(one text delta + a final usage chunk + [DONE]) to exercise the case's
plumbing WITHOUT the real endpoint or a real key:

    python3 eval/tools/fake_paas_glm.py                  # cached 600
    CACHED=0  python3 eval/tools/fake_paas_glm.py        # negative: no hits
    OMIT_DET=1 python3 eval/tools/fake_paas_glm.py       # negative: wire
        # omits prompt_tokens_details (the "client dropped it" shape)
    PROMPT=0 COMPLET=0 OMIT_DET=1 python3 eval/tools/fake_paas_glm.py
                                                        # negative: cost $0

Then point the case at it (fake key, port default 18080):

    GLM_API_KEY=fake \\
    SEEK_EVAL_GLM_BASE_URL=http://127.0.0.1:18080/chat/completions \\
    eval/run.sh glm-lane-contract ./seek

The positive shape must PASS; the negatives must FAIL on
min_cache_hit_tokens / nonzero_exits respectively — that is the proof
the assertions bite. The runner's optional SEEK_EVAL_GLM_BASE_URL hook
exists for exactly this; real runs leave it unset so the pinned
bigmodel.cn URL is exercised.

Stdlib only (http.server); env-steered so one file serves all shapes.
"""
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer

CACHED = int(os.environ.get("CACHED", "600"))
OMIT = bool(os.environ.get("OMIT_DET", ""))
PROMPT = int(os.environ.get("PROMPT", "1000"))
COMPLET = int(os.environ.get("COMPLET", "12"))
PORT = int(os.environ.get("PORT", "18080"))

USAGE = {"prompt_tokens": PROMPT, "completion_tokens": COMPLET}
if not OMIT:
    USAGE["prompt_tokens_details"] = {"cached_tokens": CACHED}

CHUNKS = [
    '{"id":"chatcmpl-fake","model":"glm-5.3","choices":[{"index":0,"delta":{"role":"assistant","content":"GLM LANE OK"},"finish_reason":null}]}',
    '{"id":"chatcmpl-fake","model":"glm-5.3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":'
    + json.dumps(USAGE) + '}',
    '[DONE]',
]


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        self.rfile.read(n)
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
