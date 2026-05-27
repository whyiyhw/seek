// readonly.go — classify bash commands as "side-effect-free" so the
// plan-analyze substate can run a narrow set of inspectors (go vet,
// go list, npm ls, …) without forcing the user to leave plan mode.
//
// Design constraints.
//
//   - Conservative. False positives (allowing a write-capable command)
//     are far worse than false negatives (denying a safe one). When in
//     doubt, return false.
//
//   - No execution-aware reasoning. We don't try to model what each
//     binary actually does — just match a small whitelist of (first
//     token, second token) pairs known to be side-effect-free in
//     plausible flag combinations.
//
//   - Metachar lockout. Shell metacharacters (`;`, `&&`, `|`, `>`, `<`,
//     backtick, `$(`, `${`, newline, `(`, `)`) force a hard false even
//     if the first token is whitelisted — `go vet; rm -rf /` matches
//     "go vet" but is obviously not safe. The check is intentionally
//     stricter than the spec allows; the alternative is a real shell
//     parser, which is many pages of code we don't need for the
//     handful of commands plan-analyze actually wants.
//
//   - No env extension. SEEK_PLAN_READONLY_BASH was proposed in the
//     design but deferred — the hardcoded list covers go / node / make
//     / which inspectors, which is enough for the use cases that
//     prompted the feature. Env-driven extension is an obvious place
//     to put a foot-gun and can be added later if a real need
//     surfaces.

package bash

import "strings"

// isReadOnlySafe reports whether cmd is one of the whitelisted
// side-effect-free inspector commands. Returns false on any metachar
// hit (chaining, redirection, substitution) or on any token outside
// the whitelist. The result is consumed by permission.Check in
// ModePlan to allow the call without ModePlan's blanket bash deny.
func isReadOnlySafe(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	if hasShellMetachars(cmd) {
		return false
	}
	tokens := strings.Fields(cmd)
	if len(tokens) == 0 {
		return false
	}

	first := tokens[0]
	rest := tokens[1:]

	switch first {
	case "go":
		if len(rest) == 0 {
			return false
		}
		switch rest[0] {
		case "vet", "list", "env", "version", "doc", "mod":
			// `go mod` has read-only subcommands (download, graph,
			// verify) plus mutating ones (tidy, init). Restrict to
			// the read-only subset.
			if rest[0] == "mod" {
				if len(rest) < 2 {
					return false
				}
				switch rest[1] {
				case "download", "graph", "verify", "why":
					return true
				}
				return false
			}
			return true
		case "build":
			// Only the dry-run form (`-n` anywhere in the args) is
			// safe — without `-n`, go build writes to the build
			// cache and emits binaries.
			return hasFlag(rest[1:], "-n")
		}
		return false

	case "npm", "pnpm":
		if len(rest) == 0 {
			return false
		}
		return rest[0] == "ls" || rest[0] == "list"

	case "yarn":
		return len(rest) > 0 && rest[0] == "list"

	case "make":
		// Dry-run mode (-n / --just-print) — no targets actually run.
		return hasFlag(rest, "-n") || hasFlag(rest, "--just-print")

	case "which", "type":
		return len(rest) > 0

	case "command":
		// `command -v <bin>` is the POSIX equivalent of `which`.
		return len(rest) >= 2 && rest[0] == "-v"
	}

	return false
}

// hasShellMetachars returns true when cmd contains any character that
// could break out of the single-command interpretation: chaining,
// piping, redirection, command/variable substitution, subshells, or
// newlines.
func hasShellMetachars(cmd string) bool {
	if strings.ContainsAny(cmd, ";|&<>`\n\r()") {
		return true
	}
	if strings.Contains(cmd, "$(") || strings.Contains(cmd, "${") {
		return true
	}
	return false
}

// hasFlag reports whether tokens contains the literal flag (e.g. "-n").
// Only exact matches count — a flag-style value like "-not-this" does
// not match "-n".
func hasFlag(tokens []string, flag string) bool {
	for _, t := range tokens {
		if t == flag {
			return true
		}
	}
	return false
}
