#!/usr/bin/env bash
# teardown.sh — compact-ab. The runner keeps everything under an
# isolated scratch (eval/tmp/compact-ab); drop it so repeated runs start
# clean. The fixture endpoint is killed by the runner's own EXIT trap.
set -u
rm -rf "${SEEK_EVAL_REPO_ROOT:-$PWD}/eval/tmp/compact-ab"
exit 0
