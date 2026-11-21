### `mktemp -t name` is BSD-only — GNU coreutils reads the argument as a template and dies "too few X's"
- **Saw**: `scripts/web-gate.sh` (runs on ubuntu-latest) failed at `mktemp -t seek-size` with "too few X's"; under `set -e` the whole gate went red before asserting anything.
- **Why**: BSD mktemp treats `-t`'s argument as a PREFIX; GNU coreutils (and POSIX's newer `-t` spec) treat it as a TEMPLATE that must contain trailing X's. The portable shape is an explicit path with X's, honoring `TMPDIR`.
- **Fix**: `mktemp "${TMPDIR:-/tmp}/seek-size.XXXXXX"` (repo precedent: `install-serve.sh:68`, `eval-trend.sh:300`); same commit swept the file's `\s` regexes (grep + awk) to `[[:space:]]` — mawk doesn't take `\s`.
- **Lesson**: scripts meant for both macOS and CI never use `mktemp -t <name>`; use the `"${TMPDIR:-/tmp}/<name>.XXXXXX"` form, and keep regexes POSIX-class (`[[:space:]]`) when mawk is in the audience.
- **Refs**: `scripts/web-gate.sh` `build_size` / `baseline`; `.github/workflows/ci.yml` web-gate job.
