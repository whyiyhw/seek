#!/usr/bin/env python3
"""compact-ab-fixture.py — seed the two A/B arms for eval/cases/compact-ab.

WHY THIS EXISTS

`/compact` runs only inside the interactive TUI (and auto-compact is
TUI-only by design — docs/guide-budget.md §2.5), so `seek -p` cannot
compact anything. The A/B has to be built from SESSION FILES: two
transcripts that differ in exactly one respect — whether the history
reaching the model is the full conversation or the `summary pair` that
`/compact` leaves behind.

`handleCompactDone` (internal/tui/update_agent.go) resets the child
session to exactly two messages:

    user      "Here is a summary of our earlier conversation. Continue
              from this context:\\n\\n<summary>"
    assistant "Understood — I have the context. Ready to continue."

Arm B reproduces that pair verbatim; the summary text is this script's
only degree of freedom (`--summary good|bad`). Everything else — model,
cwd, header shape — is identical between the arms, which is what makes
the comparison mean anything.

Both sessions are written to $SEEK_HOME/sessions/ (the caller points
SEEK_HOME at an isolated scratch dir), and arm B carries `parent_id`
→ arm A so the transcripts also read correctly through
scripts/transcript-stats (the compact-chain metrics it computes are the
offline twin of this case).

Usage:
    python3 eval/tools/compact-ab-fixture.py --home DIR --arm-a SID_A --arm-b SID_B --summary good|bad
Prints one JSON object: {"arm_a": path, "arm_b": path, "facts": [...]}
"""
import argparse
import json
import os

# The three facts a correct follow-up answer must carry. They live ONLY
# in arm A's history and (for the good variant) arm B's summary — never
# in the follow-up prompt, so "the answer mentioned it" can only come
# from the history that arm received.
FACTS = ["internal/csvexport/export.go", "report.csv", "45s"]

GOOD_SUMMARY = (
    "We are working on the CSV export path. Key facts established:\n"
    "- The exporter lives in internal/csvexport/export.go.\n"
    "- It writes its output to report.csv in the working directory.\n"
    "- The flush timeout constant is 45s.\n"
    "No open questions; next step is to answer the follow-up."
)

# The negative arm drops every fact and keeps only narration. This is the
# shape a too-aggressive summary produces: it reads like a briefing,
# proves nothing.
BAD_SUMMARY = (
    "We talked about some export code earlier and looked at a Go file. "
    "Tests were not run. Ready to continue with the next question."
)

SUMMARY_PREFIX = ("Here is a summary of our earlier conversation. "
                  "Continue from this context:\n\n")

HISTORY = [
    {"role": "user", "content": "We need to debug the CSV export path."},
    {"role": "assistant", "content": None,
     "tool_calls": [{"id": "c1", "type": "function", "function": {
         "name": "read", "arguments": json.dumps(
             {"path": "internal/csvexport/export.go", "offset": 1})}}]},
    {"role": "tool", "tool_call_id": "c1",
     "content": "export.go: the exporter writes report.csv; const timeout = 45s"},
    {"role": "assistant",
     "content": "So internal/csvexport/export.go writes report.csv and the "
                "flush timeout is 45s."},
]


def header(sid, cwd, model, turns, parent_id=None):
    h = {
        "schema_version": 3, "id": sid,
        "created_at": "2026-11-24T10:00:00Z", "updated_at": "2026-11-24T10:05:00Z",
        "model": model, "yolo": True, "cwd": cwd, "system_prompt": "",
        "turns": turns, "tool_calls": 2,
        "usage": {"prompt_tokens": 1200, "completion_tokens": 120,
                  "total_tokens": 1320},
    }
    if parent_id:
        h["parent_id"] = parent_id
    return h


def write_session(path, hdr, msgs):
    with open(path, "w", encoding="utf-8") as f:
        f.write(json.dumps(hdr, ensure_ascii=False) + "\n")
        for m in msgs:
            f.write(json.dumps(m, ensure_ascii=False) + "\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--home", required=True)
    ap.add_argument("--arm-a", required=True)
    ap.add_argument("--arm-b", required=True)
    ap.add_argument("--cwd", default="")
    ap.add_argument("--model", default="deepseek-v4.1-flash")
    ap.add_argument("--summary", choices=["good", "bad"], default="good")
    args = ap.parse_args()

    cwd = args.cwd or os.path.abspath(os.getcwd())
    sdir = os.path.join(args.home, "sessions")
    os.makedirs(sdir, exist_ok=True)

    arm_a = os.path.join(sdir, args.arm_a + ".jsonl")
    arm_b = os.path.join(sdir, args.arm_b + ".jsonl")

    write_session(arm_a, header(args.arm_a, cwd, args.model, turns=2), HISTORY)

    summary = GOOD_SUMMARY if args.summary == "good" else BAD_SUMMARY
    pair = [
        {"role": "user", "content": SUMMARY_PREFIX + summary},
        {"role": "assistant",
         "content": "Understood — I have the context. Ready to continue."},
    ]
    write_session(arm_b, header(args.arm_b, cwd, args.model, turns=2,
                                parent_id=args.arm_a), pair)

    print(json.dumps({"arm_a": arm_a, "arm_b": arm_b, "facts": FACTS}))


if __name__ == "__main__":
    main()
