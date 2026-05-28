package routines

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// LoadEnvFile parses a minimal dotenv-style file at path:
//
//   - one KEY=VALUE per non-blank, non-comment line
//   - lines starting with `#` (after optional leading whitespace)
//     are comments
//   - leading/trailing whitespace on KEY and around the surrounding
//     line is trimmed; whitespace INSIDE VALUE is preserved
//   - VALUE may be wrapped in single or double quotes, which are
//     stripped on parse; neither form does shell-style escape
//     interpretation (\n / \t stay literal). This matches systemd
//     EnvironmentFile= semantics and is intentionally simpler than
//     POSIX shell — we don't want to bring in a shell-grammar parser
//     to handle env injection.
//   - duplicate keys: LAST occurrence wins (matches `cmd.Env` semantics
//     so the file's order intuitively corresponds to override
//     precedence)
//
// Returns (nil, nil) when the file does not exist — the env file is
// an OPT-IN feature (feature-routines.md §3.8 G3). Callers should
// not log or warn on absence.
//
// Returns an error only when:
//   - path is non-empty and the file exists but is unreadable
//   - a non-comment, non-blank line is missing `=`
//   - the KEY portion is empty (e.g. `=value`)
//
// The strictness is deliberate: a typo in the env file is almost
// always more dangerous than a missing file (the OS scheduler will
// inherit nothing and seek will fail loudly on missing API key;
// silently dropping `KEY-WITH-DASH=v` because we tolerated bad syntax
// gives the user a mis-priced or auth-failing tick with no signal).
func LoadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("routines: open env file %s: %w", path, err)
	}
	defer f.Close()

	out := make(map[string]string)
	sc := bufio.NewScanner(f)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("routines: env file %s:%d missing '=' in %q", path, lineNum, raw)
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			return nil, fmt.Errorf("routines: env file %s:%d empty key in %q", path, lineNum, raw)
		}
		val := line[eq+1:]
		// Strip wrapping quotes if balanced. Unbalanced quotes are
		// left as literal — a value containing a stray `"` should
		// stay queryable in the child process.
		if n := len(val); n >= 2 {
			first, last := val[0], val[n-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : n-1]
			}
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("routines: read env file %s: %w", path, err)
	}
	return out, nil
}

// MergeEnv returns base with overlay keys appended in iteration
// order. Because Go's exec.Cmd resolves duplicate env keys by
// last-wins (documented in os/exec), appending is the correct way
// to make overlay entries override inherited base.
//
// Returns a new slice — base is not mutated. Empty overlay returns
// (a copy of) base unchanged so callers can unconditionally call
// this in DefaultSubprocess without a nil-check branch.
//
// Sort is deterministic (overlay keys appended in alpha order) so
// `cmd.Env` is byte-stable for a given (base, overlay) — useful for
// tests that snapshot env, and trivial to verify.
func MergeEnv(base []string, overlay map[string]string) []string {
	if len(overlay) == 0 {
		out := make([]string, len(base))
		copy(out, base)
		return out
	}
	out := make([]string, 0, len(base)+len(overlay))
	out = append(out, base...)
	// Deterministic order: sort keys for byte-stable cmd.Env. Cheap
	// (overlay rarely > 10 entries) and makes test assertions easy.
	keys := make([]string, 0, len(overlay))
	for k := range overlay {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, k+"="+overlay[k])
	}
	return out
}
