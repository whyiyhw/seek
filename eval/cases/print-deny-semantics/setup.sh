#!/usr/bin/env bash
# Scratch dir for the two marker files (bash-side / write-side), inside
# CWD so the write tool's in-CWD default-allow path applies.
set -e
mkdir -p eval/tmp/print-deny-semantics
