#!/usr/bin/env bash
# Clean up the scratch dir (eval/run.sh teardown hook — runs even when
# the seek invocation failed). Nothing is scored from disk: the three
# sentinel values live in the results row's final_text.
set -u
rm -rf eval/tmp/parallel-fanout
