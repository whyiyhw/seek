#!/usr/bin/env bash
# Clean up the scratch dir (eval/run.sh teardown hook — runs even when
# the seek invocation failed). Nothing to score from disk: the answer
# (count + top-3 codes) lives in the results row's final_text and is
# scored offline against the gold in README.md.
set -u
rm -rf eval/tmp/grep-refine-not-flood
