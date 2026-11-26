### jq without input files and without -n runs the filter zero times — empty output, exit 0
- **Saw**: `table=$(jq -r --slurpfile a f1 --slurpfile b f2 '<program>')` in a script returned an EMPTY string with exit code 0 — not even constant-string lines printed — so it shipped a header-only "table"
- **Why**: with no input files and no `-n`, jq runs the filter once per JSON value on **stdin**; a script has no stdin lines, so the filter executes 0 times. (Sibling trap: `--slurpfile v FILE` makes `$v` the array of the file's JSON values — `$v[0]` is the first VALUE, not the value array; `map(.case)` over a row object dies with `Cannot index string with string "case"`.)
- **Fix**: `jq -n -r --slurpfile …` in `eval/ablate.sh`, plus an explicit `[[ -z "$table" ]] && exit 2` guard so a silent-empty can't pass as success. `$v` used directly as the row array.
- **Lesson**: a jq program whose inputs are all `--slurpfile` variables needs `-n`; and any `$(jq …)` feeding a generated artifact should assert non-empty — jq has failure modes that look exactly like success
- **Refs**: `eval/ablate.sh` (the `-n` note above the table jq + the empty-table guard)
