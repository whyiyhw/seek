#!/bin/bash
# anchor-docs-consistency check — grep invariants: stale tokens must be
# gone everywhere except SOURCE_OF_TRUTH.md; canonical tokens present.
set -u
stale=$(grep -rn -e ':8081' -e '8081' -e '45s' -e '500/day' docs/ \
  | grep -v 'SOURCE_OF_TRUTH.md' || true)
if [ -n "$stale" ]; then
  echo "check: stale values remain:" >&2
  printf '%s\n' "$stale" >&2
  exit 1
fi
grep -rq '8080' docs/ && grep -rq '30s' docs/ && grep -rq '100/day' docs/ || {
  echo "check: a canonical value disappeared entirely" >&2
  exit 1
}
# SOURCE_OF_TRUTH.md itself must be untouched.
case_dir=$(cd "$(dirname "$0")" && pwd)
cmp -s docs/SOURCE_OF_TRUTH.md "$case_dir/workspace/docs/SOURCE_OF_TRUTH.md" || {
  echo "check: SOURCE_OF_TRUTH.md was modified" >&2
  exit 1
}
