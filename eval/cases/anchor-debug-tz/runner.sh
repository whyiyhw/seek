#!/bin/bash
# anchor shim (eval v3 A1) — mechanics live in eval/tools/anchor-runner.sh
export SEEK_EVAL_ANCHOR_DIR="$(cd "$(dirname "$0")" && pwd)"
exec bash "${SEEK_EVAL_ANCHOR_DIR}/../../tools/anchor-runner.sh"
