#!/usr/bin/env bash
# Copy the fixture into a per-case scratch dir so the model can mutate
# it without dirtying the committed fixture (eval/run.sh setup hook).
set -e
mkdir -p eval/tmp/write-guard-fallback
cp eval/cases/write-guard-fallback/testdata/config.yaml \
  eval/tmp/write-guard-fallback/config.yaml
