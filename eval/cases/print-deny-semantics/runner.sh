#!/usr/bin/env bash
# runner.sh — print-deny-semantics. Replaces the default harness
# invocation, which passes -yolo. This case must run with NO -yolo so
# the initial preference stays PrefDeny (cmd/seek/main.go:
# `initialPref := permission.PrefDeny`; only an explicit --yolo upgrades
# to PrefYolo) — exactly the shape a remote control plane gets when it
# execs `seek -p --json` without --yolo (PRD D-C5).
#
# The contract under test (c_7hudp6 实测, PRD §6 已验证②):
#   - bash is gated: a non-ReadOnly-whitelisted command comes back as
#     an ErrDenied tool RESULT ("permission denied"), never a fatal
#     error — the agent adjusts and the run finishes;
#   - in-CWD write is allowed (not a "dangerous" action);
#   - the process exits 0 — no hang waiting for a TUI approval that
#     can't exist in print mode.
#
# Everything else matches the default invocation (-no-save: this case
# doesn't assert session_id). Env contract (eval/run.sh):
# SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT, SEEK_EVAL_PROMPT_FILE,
# SEEK_EVAL_MAX_TURNS.
set -u
cd "$SEEK_EVAL_REPO_ROOT"

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")
"$SEEK_EVAL_BINARY" -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" -json -p "$prompt"
rc=$?
printf '{"type":"runner_fact","exit_code":%d}\n' "$rc"
