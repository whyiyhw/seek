#!/usr/bin/env bash
# Remove the case scratch (sandbox SEEK_HOME, bound project dir, seek
# binary copy, captured SSE/serve/rpc logs). eval/run.sh runs this even
# when the invocation failed. Orphaned serve/curl/python processes are
# the runner's own trap's job; this only removes files.
rm -rf eval/tmp/serve-web-stream
