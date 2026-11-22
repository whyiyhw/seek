#!/usr/bin/env bash
# teardown.sh — plan-e2e. The runner already runs under an isolated
# scratch (eval/tmp/plan-e2e); drop it so repeated runs start clean.
# The fixture endpoint is killed by the runner's own EXIT trap.
set -u
rm -rf "${SEEK_EVAL_REPO_ROOT:-$PWD}/eval/tmp/plan-e2e"
exit 0
