### `find` lists piped into awk argv split filenames at spaces — use -print0 | sort -z | xargs -0
- **Saw**: `pitfall-to-eval.sh` full run aborted with awk/stat errors on entries whose filenames contain spaces (`docs/pitfalls/go/体积记账的 delta 锚点….md`), paths truncated at the first space.
- **Why**: `files=$(find …)` handed to `awk '…' $files` splits on IFS whitespace in argv expansion — filenames in this repo legally contain spaces, CJK, and em-dashes.
- **Fix**: `find … -print0 | sort -z >"$list"` + `xargs -0 awk … "$prog" <"$list"` (mktemp list, c_bohzri); full run then 0 awk errors.
- **Lesson**: in repos where filenames carry spaces/CJK, pass file lists NUL-separated end to end (-print0 / sort -z / xargs -0); a newline-separated list is only safe for lists you control.
- **Refs**: `scripts/pitfall-to-eval.sh:parse_entries`
