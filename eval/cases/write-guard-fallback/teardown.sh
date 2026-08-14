#!/usr/bin/env bash
# Record the final file state for offline correctness scoring, then
# clean up the scratch dir (eval/run.sh teardown hook — runs even when
# the seek invocation failed).
log="eval/tmp/write-guard-fallback/outcome-$(date -u +%Y%m%dT%H%M%S).txt"
if [ -f eval/tmp/write-guard-fallback/config.yaml ]; then
  if grep -q "managed by seek" eval/tmp/write-guard-fallback/config.yaml; then
    echo "content_ok=1" > "$log"
  else
    echo "content_ok=0" > "$log"
  fi
  rm -f eval/tmp/write-guard-fallback/config.yaml
else
  echo "content_ok=0" > "$log"
fi
