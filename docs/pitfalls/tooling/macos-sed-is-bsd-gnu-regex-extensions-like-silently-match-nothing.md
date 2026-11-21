### macOS sed is BSD: GNU regex extensions like `\?` silently match nothing
- **Saw**: a `sed -n 's/.*"code":\(-\?[0-9]*\).*/\1/p'` extraction in a bash test script returned EMPTY on macOS (the value never printed), while the same expression worked on Linux CI
- **Why**: BSD sed (what macOS ships, same Bash 3.2 world `web-gate.sh` already pins) does not support GNU's `\?` "zero or one" in basic regex — it parses as a literal-ish no-op that never matches. No error, no warning; the substitution just never fires
- **Fix**: replace the sed with `grep -o '"code":-[0-9]*' | head -1 | cut -d: -f2` (`scripts/web-rename-manual.sh` `getcode()`); `grep -o` + `cut` is portable across both seds
- **Lesson**: in repo shell scripts, extraction by pattern belongs to `grep -o`/`awk`, not `sed s///` with GNU-flavored regex — the failure mode is silent emptiness, which reads as "server returned nothing" and sends you debugging the wrong layer
- **Refs**: `scripts/web-rename-manual.sh`; `scripts/web-gate.sh` header ("Bash 3.2 compatible … macOS ships 3.2")
