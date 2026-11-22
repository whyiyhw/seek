// stripcd.go — drop a redundant `cd <session cwd> && ` prefix before
// the command reaches the shell.
//
// The measurement that motivated it (2026-11-22, 40 sessions / 5,015
// bash calls): 18.6% of bash calls opened with `cd `, and 322 of them
// (6.4% of all bash) cd'd into the SESSION'S OWN working directory —
// exactly what the tool contract already guarantees, since cmd.Dir is
// pinned to policy.CWD(). The system prompt says "DO NOT prepend cd",
// and it says it in the prompt prefix where it is free; the model does
// it anyway. Two hand-written teaching mechanisms (the success-path
// advisory and the plan-analyze denial hint) had measured compliance
// rates of 18.6% and 20.6% respectively — i.e. the reminder does not
// work, so remove the need for it instead.
//
// Scope, deliberately narrow:
//
//   - Only a LEADING `cd <path> &&`. Not `;` (a `cd X; cmd` chain means
//     cmd runs regardless, and the shapes diverge when a later reader
//     edits X), not `||` (a fallback branch), not a cd that appears
//     later in the chain (that one is a real scratch-dance,
//     `cd /tmp && …`).
//   - Only when <path> resolves to the session cwd. Any other path is
//     the model deliberately changing directory and must be honoured.
//   - Only the EXECUTED string. The header echo keeps the original
//     command verbatim: the model wrote it, the transcript must show
//     what it wrote, and a test asserts the echo.
//
// What this does NOT do: it does not rewrite the model's command text
// anywhere the model can read it back. There is no "your command was
// rewritten" message in the result beyond the header marker, because a
// rewrite notice is itself transcript bytes on every later request, and
// the only consumer of the rewrite is the shell.

package bash

import (
	"path/filepath"
	"runtime"
	"strings"
)

// stripRedundantCD returns (command-to-run, true) when the command had a
// leading `cd <cwd> && ` prefix that was removed, else (command, false).
// cwd is the directory the shell will run in (policy.CWD()); an empty
// cwd disables stripping — without a target to compare against, every
// cd must be honoured.
func stripRedundantCD(command, cwd string) (string, bool) {
	if cwd == "" {
		return command, false
	}
	trimmed := strings.TrimSpace(command)
	rest, ok := cutCDPrefix(trimmed)
	if !ok {
		return command, false
	}
	path, after, ok := cutFirstWord(rest)
	if !ok {
		return command, false
	}
	// Require the `&&` separator immediately after the path: `cd X; cmd`
	// and `cd X || cmd` are different control flows and are left alone.
	after = strings.TrimLeft(after, " \t")
	after, ok = strings.CutPrefix(after, "&&")
	if !ok {
		return command, false
	}
	remainder := strings.TrimSpace(after)
	if remainder == "" {
		return command, false // `cd X &&` with nothing after — let the shell judge it
	}
	if !sameDir(unquoteShellWord(path), cwd) {
		return command, false
	}
	return remainder, true
}

// cutCDPrefix splits a leading `cd ` / `cd\t` off. Mirrors hintCDPrefix
// so the hint and the stripper can never disagree about what counts as a
// cd prefix.
func cutCDPrefix(s string) (string, bool) {
	if !hintCDPrefix(strings.ToLower(s)) {
		return "", false
	}
	return s[len("cd"):], true
}

// cutFirstWord takes the first whitespace-delimited run and returns it
// plus the remainder. Quote-aware for the common `cd "a b" && …` form;
// an unterminated quote means "not a shape we understand" and the
// caller leaves the command alone.
func cutFirstWord(s string) (word, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", false
	}
	if q := s[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(s[1:], q)
		if end < 0 {
			return "", "", false
		}
		return s[1 : 1+end], s[2+end:], true
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], s[i:], true
	}
	return s, "", true
}

// unquoteShellWord strips one layer of matching quotes, if present.
func unquoteShellWord(w string) string {
	if len(w) >= 2 {
		if (w[0] == '"' && w[len(w)-1] == '"') || (w[0] == '\'' && w[len(w)-1] == '\'') {
			return w[1 : len(w)-1]
		}
	}
	return w
}

// sameDir reports whether path denotes the same directory as cwd.
//
// Deliberately conservative: absolute paths and `.`/`./` resolve; a
// relative path other than `.` does NOT (resolving it would mean
// guessing at symlinks and at the shell's own cwd semantics, and a wrong
// guess executes the command somewhere the model did not intend).
// Windows compares case-insensitively.
func sameDir(path, cwd string) bool {
	if path == "" {
		return false
	}
	// A bare "." always means the shell's cwd, which IS cwd.
	if path == "." || path == "./" {
		return true
	}
	if !filepath.IsAbs(path) {
		// `~` and relative subdirs are left untouched on purpose.
		return false
	}
	a := filepath.Clean(path)
	b := filepath.Clean(cwd)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
