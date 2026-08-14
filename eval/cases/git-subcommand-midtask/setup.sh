#!/usr/bin/env bash
# Scratch file for the mid-task variant. Deliberately OUTSIDE eval/tmp —
# that dir is gitignored and the case needs the file visible to git
# status (an untracked "?? " entry is enough; diff --stat showing
# nothing is fine, the case measures call SHAPE, not git semantics).
set -eu
mkdir -p git-shape-scratch
printf 'version: v1\nnotes for the git-subcommand-midtask eval\n' > git-shape-scratch/notes.txt
