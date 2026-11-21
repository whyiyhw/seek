### macOS bash 3.2 under set -u mangles $VAR followed by a multi-byte character ("TMP\ufeff: unbound variable")
- **Saw**: manual-test script died in cleanup with `line 60: TMP<EFBBBF>: unbound variable` — no BOM exists in the file.
- **Why**: `/bin/bash` on macOS is 3.2 (no UTF-8 name parsing); under `set -u`, `$TMP（` reads lead bytes of the full-width character into the variable NAME, then reports that phantom name unset. Reproducible: `bash -uc 'TMP=x; echo "$TMP（"'`.
- **Fix**: brace every `${VAR}` whose next character is non-ASCII (the KEEP message in `scripts/web-sessions-manual.sh`); commit with card `c_hinacy`.
- **Lesson**: repo shell scripts must stay bash-3.2-safe (macOS default) — `${VAR}` braces are mandatory before CJK punctuation, and self-check any script whose failure path only runs in cleanup traps.
- **Refs**: `scripts/web-sessions-manual.sh` cleanup; `scripts/serve-smoke-test.sh` (same bash-3.2 audience).
