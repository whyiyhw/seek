#!/usr/bin/env bash
# Clean up the scratch dir (eval/run.sh teardown hook — runs even when
# the seek invocation failed). Nothing is scored from disk: the answers
# live in the results row's final_text and are compared against the gold
# in README.md.
set -u
rm -rf eval/tmp/grep-affordance
