### go list stops flag-parsing at the first package argument — a trailing `-f` becomes a package named "-f"
- **Saw**: `scripts/kernel-deps.sh --self-test` failed with "manufactured violation not reported" while a hand-run of the same report worked; the built-in sanity check read `go list resolved 3/1 kernel packages`.
- **Why**: the `go` command parses flags only BEFORE the first package argument; `go list -e $PKGS -f '{{...}}'` turns `-f` and the template into two extra package PATTERNS (with `-e` they "resolve" to themselves), so the output gains bogus lines and the intended `-f` never applies — no flag error is ever raised.
- **Fix**: all flags before patterns — `go list -e -f '{{...}}' $PKGS` in `scripts/kernel-deps.sh`; commit with card `c_zcygo2`.
- **Lesson**: in `go` tool invocations flags precede patterns, always; a wrong-arg-position bug masquerades as extra/missing packages, not a flag complaint.
- **Refs**: `scripts/kernel-deps.sh` (comment at the resolved-count check).
