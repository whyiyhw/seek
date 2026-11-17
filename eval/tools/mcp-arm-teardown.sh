#!/usr/bin/env bash
# mcp-arm-teardown.sh — per-arm teardown for the mcp-semble-routing
# case group: drop the arm's scratch SEEK_HOME. The group-level
# .semble-prewarmed marker is deliberately KEPT (the semble index
# cache outlives one case; only the per-run homes are disposable).
# Invoked (from repo root) as: mcp-arm-teardown.sh <a1|a2|a3>
set -u
arm=${1:-}
case "$arm" in
  a1|a2|a3) ;;
  *) exit 0 ;;
esac
rm -rf "$PWD/eval/tmp/mcp-semble-routing/$arm"
