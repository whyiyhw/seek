### macOS bash 3.2 has no `mapfile` / `readarray`
- **Saw**: `.githooks/pre-commit` died with `mapfile: command not found` on a fresh clone, blocking every commit
- **Why**: `mapfile` and its alias `readarray` are bash 4+ builtins. Apple stopped shipping newer bash with macOS (post-3.2 is GPLv3-licensed); `/bin/bash` is permanently 3.2 and `#!/usr/bin/env bash` happily resolves to it unless the user has Homebrew bash first in PATH
- **Fix**: replaced both `mapfile -t arr < <(...)` calls with `arr=(); while IFS= read ...; do arr+=("$line"); done < <(...)` — bash 3.2 supports arrays + process substitution, just not `mapfile`
- **Lesson**: any `#!/usr/bin/env bash` script that ships to a team with macOS users must stick to bash 3.2 features. Reach for `read` loops; avoid `mapfile`/`readarray`, `${var^^}`/`${var,,}`, `declare -n` namerefs, and `BASH_REMATCH` in extglob contexts
- **Refs**: `.githooks/pre-commit`
