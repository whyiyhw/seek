#!/bin/bash
# anchor-config-migrate check — jq asserts exact paths, types, values
# and key order.
set -u
command -v jq >/dev/null 2>&1 || { echo "check: jq required" >&2; exit 1; }
[ -f v2.json ] || { echo "check: v2.json missing" >&2; exit 1; }
jq -e '
  (.server.port == 8080) and (.server.port | type == "number")
  and (.server.host == "localhost")
  and (.server.timeout_s == 30) and (.server.timeout_s | type == "number")
  and (.log.level == "info") and (.log.file == "app.log")
  and (.retry.max_attempts == 3) and (.retry.max_attempts | type == "number")
  and (.retry.backoff_ms == 200) and (.retry.backoff_ms | type == "number")
  and ((keys_unsorted) == ["server", "log", "retry"])
  and ((.server | keys_unsorted) == ["port", "host", "timeout_s"])
  and ((.retry | keys_unsorted) == ["max_attempts", "backoff_ms"])
' v2.json >/dev/null || { echo "check: v2.json fails the migration asserts" >&2; exit 1; }
