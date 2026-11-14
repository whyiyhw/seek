#!/usr/bin/env bash
# teardown.sh — glm-lane-contract. Removes the isolated SEEK_HOME
# scratch (copied config.json + saved sessions) under gitignored
# eval/tmp/. run.sh runs this even when the case failed.
rm -rf "$(dirname "$0")/../../tmp/glm-lane-contract"
