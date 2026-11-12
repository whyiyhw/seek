#!/usr/bin/env bash
# Remove the isolated SEEK_HOME (copied config.json + the run's session
# files). eval/run.sh runs this even when the invocation failed.
rm -rf eval/tmp/session-id-contract
