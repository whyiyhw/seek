# v1 → v2 config migration rules

Produce `v2.json` in the current directory from `v1.conf`:

1. Dotted sections nest: `server.port = 8080` becomes
   `{"server": {"port": 8080}}`.
2. Numbers stay numbers (8080, not "8080"); strings stay strings.
3. Rename: `retry.max` becomes `retry.max_attempts` (same value).
4. Add absent-with-default keys:
   - `server.timeout_s` = 30 (v1 has no such key)
5. Drop comments and blank lines; v2.json is valid JSON with keys in
   the same order as v1's appearance (server.*, log.*, retry.*) with
   `server.timeout_s` right after `server.host`.
