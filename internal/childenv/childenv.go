// Package childenv builds environments for child processes seek spawns.
//
// # Why this exists
//
// Go's os/exec inherits the ENTIRE parent environment when cmd.Env is
// nil. That default is fine for processes whose argv seek fully controls
// (internal `git` calls, notifications), but it is wrong for the class of
// children whose command or code comes from somewhere else:
//
//   - the bash tool — argv is chosen by the MODEL, and anything the
//     command transitively runs (npm postinstall, Makefile recipe, build
//     script) inherits too
//   - MCP / LSP servers — third-party binaries named in user config
//   - git operations on cloned skill repos — a repo can ship hooks
//   - user shell hooks — arbitrary commands on tool events
//
// For those, inheriting the full environment hands DEEPSEEK_API_KEY,
// GITHUB_TOKEN, OPENAI_API_KEY and friends to code seek does not control.
// The OS sandbox does not help here: seatbelt/landlock confine FILE
// effects, not environment inheritance (dsh draws the same line — its
// sandbox vocabulary is explicitly file-only). So the scrub has to happen
// where the process is built.
//
// The rule, borrowed from deepseek-harness's `scrubbedParentEnv`
// (packages/subprocess/subprocess/src/index.ts:44-65): variables whose
// NAME looks credential-bearing, plus seek's own SEEK_* namespace, are
// never passed implicitly. Anything a child genuinely needs must be
// passed EXPLICITLY — via the keep list or by appending to the result.
//
// # Deliberate exclusions
//
// Two spawn paths do NOT use this package, both on purpose:
//
//   - internal/routines/tick.go re-spawns `seek` itself for a scheduled
//     run. It needs the API key; withholding it would just make every
//     cron tick fail auth.
//   - internal/hooks (baseEnv) runs the user's own shell snippets from
//     their own settings file — same trust level as their .bashrc, and
//     hooks commonly exist precisely to reach a credentialed service.
//     See the rationale comment on ShellRunner.baseEnv.
//
// Adding a new exec site? Default to scrubbing. Skipping it is a
// decision that belongs in a comment at the call site, not an omission.
//
// # Name-based, not value-based
//
// The predicate matches on variable names, never on values. Value
// sniffing (entropy heuristics, "looks like a JWT") produces both false
// positives that break builds and false negatives that leak, and it
// would mean reading every secret into a scanner. Name matching is
// coarse but predictable, and predictability is what makes the escape
// hatch usable: a user who gets bitten can name the variable and move on.
package childenv

import (
	"os"
	"strings"
)

// sensitiveSubstrings are matched against the UPPERCASED variable name.
// Substring (not prefix) matching is deliberate: the credential-bearing
// token can appear anywhere — API_KEY, GH_TOKEN, NPM_AUTH_TOKEN,
// AWS_SECRET_ACCESS_KEY, PGPASSWORD.
//
// Kept deliberately close to dsh's KEY|PASSWORD|SECRET|TOKEN set. Two
// notable NON-entries:
//
//   - "AUTH" — would match SSH_AUTH_SOCK, and scrubbing that breaks
//     every ssh-backed `git push` the model runs. The value is a socket
//     path, not a credential.
//   - "USER" / "ACCOUNT" — identity, not authorisation.
var sensitiveSubstrings = []string{
	"KEY",
	"SECRET",
	"TOKEN",
	"PASSWORD",
	"PASSWD",
	"CREDENTIAL",
}

// sensitivePrefixes are matched against the UPPERCASED variable name.
//
// SEEK_ is seek's own configuration namespace (SEEK_HOME,
// SEEK_SESSIONS_DIR, SEEK_AUTO_DISTILL, …). None of it is secret, but
// none of it should silently steer a nested process either — a child
// `seek` invocation inheriting the parent's SEEK_HOME writes sessions
// somewhere the user did not ask for. dsh withholds DSH_* for the same
// reason.
var sensitivePrefixes = []string{
	"SEEK_",
}

// IsSensitive reports whether a variable with this name should be
// withheld from child processes. Matching is case-insensitive; an empty
// name is not sensitive (callers filter malformed entries separately).
func IsSensitive(name string) bool {
	if name == "" {
		return false
	}
	up := strings.ToUpper(name)
	for _, p := range sensitivePrefixes {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	for _, s := range sensitiveSubstrings {
		if strings.Contains(up, s) {
			return true
		}
	}
	return false
}

// Scrub returns env with sensitive entries removed, preserving the
// original order and leaving every other entry byte-identical.
//
// Names listed in keep are retained even when they look sensitive —
// that is the escape hatch for the user who genuinely needs GH_TOKEN
// visible to `gh` inside the bash tool. keep matching is
// case-insensitive and exact (no substring, no globbing): an escape
// hatch that is itself fuzzy would quietly re-leak neighbours.
//
// Entries with no "=" are dropped: they cannot be a valid child
// environment entry, and passing them through would let a malformed
// parent env produce an exec error far from its cause.
func Scrub(env []string, keep ...string) []string {
	var keepSet map[string]struct{}
	if len(keep) > 0 {
		keepSet = make(map[string]struct{}, len(keep))
		for _, k := range keep {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			keepSet[strings.ToUpper(k)] = struct{}{}
		}
	}

	out := make([]string, 0, len(env))
	for _, e := range env {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			// No "=", or an empty name ("=value"). Neither is a usable
			// child env entry.
			continue
		}
		name := e[:eq]
		if _, ok := keepSet[strings.ToUpper(name)]; ok {
			out = append(out, e)
			continue
		}
		if IsSensitive(name) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Sanitized is Scrub(os.Environ(), keep...) — the environment to hand a
// child whose command or code seek does not control.
//
// Callers needing to ADD variables should append to the result:
//
//	env := childenv.Sanitized(cfgPassthrough...)
//	env = append(env, "LSP_MODE=stdio")
//
// Appending after the scrub is intentional: cmd.Env is last-wins, so an
// explicit value always beats whatever the parent had.
func Sanitized(keep ...string) []string {
	return Scrub(os.Environ(), keep...)
}

// Withheld returns the sorted NAMES (never values) that Scrub would drop
// from env. It exists for diagnostics — `seek doctor` output, a debug
// log line, a test asserting a specific variable is covered. Values are
// never returned, so the result is safe to print.
func Withheld(env []string, keep ...string) []string {
	kept := make(map[string]struct{}, len(env))
	for _, e := range Scrub(env, keep...) {
		if eq := strings.IndexByte(e, '='); eq > 0 {
			kept[e[:eq]] = struct{}{}
		}
	}
	var out []string
	seen := make(map[string]struct{}, len(env))
	for _, e := range env {
		eq := strings.IndexByte(e, '=')
		if eq <= 0 {
			continue
		}
		name := e[:eq]
		if _, ok := kept[name]; ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
