#!/usr/bin/env bash
# mcp-semble-routing a1 arm setup — thin wrapper; engine: eval/tools/mcp-arm-setup.sh
# (setup.sh runs from repo root before the seek invocation)
exec bash "$PWD/eval/tools/mcp-arm-setup.sh" a1
