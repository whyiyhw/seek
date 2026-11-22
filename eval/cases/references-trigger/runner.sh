#!/usr/bin/env bash
# runner.sh — references-trigger. Replaces the default invocation for two
# reasons the default cannot express:
#
#   1. SKIP PATH. `references` is only useful when a language server is
#      actually installed; the trigger this case measures ("reach for the
#      semantic tool before the name grep") is a question about model
#      choice, and a missing gopls turns it into a question about the
#      machine. When no server for the repo's language is on PATH the
#      runner emits one informational fact and NO assertion facts, so the
#      row is a record, not a red — matching eval v2's "no data is not a
#      green light" by simply not claiming a result either way.
#
#   2. CWD. The case asks about seek's own source (a signature with a
#      known caller set), so the run happens in the repo root. The
#      default harness already runs there; the runner is what lets us
#      gate on the server and stream the skip fact.
#
# Env contract (eval/run.sh): SEEK_EVAL_BINARY, SEEK_EVAL_REPO_ROOT,
# SEEK_EVAL_PROMPT_FILE, SEEK_EVAL_MAX_TURNS, SEEK_EVAL_PROVIDER_ARGS.
set -u

prompt=$(cat "$SEEK_EVAL_PROMPT_FILE")

have_server=0
for bin in gopls pyright typescript-language-server; do
  if command -v "$bin" >/dev/null 2>&1; then
    have_server=1
    break
  fi
done

if [ "$have_server" -eq 0 ]; then
  # Informational, not failed: an absent language server is an environment
  # fact, and runner_assert_failures must stay 0 so the row does not read
  # as a behavioural regression. The `check` key keeps this on the
  # agent-surface channel, which is what makes the bg_* family readable.
  printf '{"type":"runner_fact","assert":"runner_precondition","failed":false,"detail":"no language server on PATH (gopls/pyright/typescript-language-server) — case skipped, not judged"}\n'
  printf '{"type":"runner_fact","invocation":0,"exit_code":0}\n'
  exit 0
fi

# shellcheck disable=SC2086  # SEEK_EVAL_PROVIDER_ARGS is a flag list by contract
"$SEEK_EVAL_BINARY" -yolo -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" -json \
  ${SEEK_EVAL_PROVIDER_ARGS-} -p "$prompt"
rc=$?
printf '{"type":"runner_fact","invocation":1,"exit_code":%d}\n' "$rc"
exit 0
