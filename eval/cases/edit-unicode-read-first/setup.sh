#!/usr/bin/env bash
# Copy the fixture into a per-case scratch dir so the model can mutate
# it without dirtying the committed fixture (eval/run.sh setup hook).
set -e
mkdir -p eval/tmp/edit-unicode-read-first
cp eval/cases/edit-unicode-read-first/testdata/panel.txt \
  eval/tmp/edit-unicode-read-first/panel.txt
