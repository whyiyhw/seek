// hint.go — context-aware error annotations for bash commands denied
// in plan-analyze, plus the success-path advisory trailer.
//
// Two mechanisms live here and they are NOT the same thing:
//
//  1. planAnalyzeBashHint — fires on a DENIAL. The command did not run,
//     so the model MUST do something different. Load-bearing: without it
//     a denied `git log` retries the same shape, or does not retry at
//     all.
//
//  2. bashAdvisory — fires on SUCCESS. The command worked; the hint is a
//     nudge about a better future shape. Measured 2026-11-22 on 40
//     sessions (6,474 tool calls) and it does not work: see the
//     per-class table on bashAdvisory below. Four of its six classes are
//     now gone; what remains is capped to one firing per class per
//     session.
//
// Why here and not in permission.go: the hint logic is bash-specific
// (it knows about `cd && X`, git-via-bash, `go test`, shell metachars
// — none of which the permission package should care about). The
// permission package returns a vanilla ErrDenied; bash.Execute wraps
// it with this hint at the tool boundary.

package bash

import "strings"

// planAnalyzeBashHint returns a context-aware suggestion for why a
// bash command was denied in plan-analyze and what to try instead.
// Combines multiple targeted hints when more than one pattern
// matches (e.g. `cd /x && git log` triggers BOTH the cd hint AND
// the git-tool hint). Falls back to the generic "use the whitelist"
// pointer when no specific pattern matches.
func planAnalyzeBashHint(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)

	var hints []string

	if hintCDPrefix(lower) {
		hints = append(hints, "drop the `cd` prefix — seek's bash tool already runs from the project root")
	}
	if hintGitViaBash(trimmed) {
		hints = append(hints, "use the `git` tool (subcommand-whitelisted, plan-mode allowed) instead of `bash(\"git …\")`")
	}
	if hintGoTest(lower) {
		hints = append(hints, "for verification use `go vet ./...` or `go build -n ./...` (whitelisted); `go test` runs code and is denied because it has side effects")
	}
	if hasShellMetachars(trimmed) {
		hints = append(hints, "remove shell metacharacters (`;`, `&&`, `|`, `>`, `` ` ``, `$(`) — chaining / redirect / substitution is never allowed in plan-analyze regardless of the rest of the command")
	}

	if len(hints) == 0 {
		// No pattern matched — the command is genuinely outside the
		// whitelist (curl, docker, kubectl, …) and the model can't
		// rewrite it as a whitelisted equivalent. Point at the THREE
		// escape paths in order of seek-nativeness:
		//
		// (1) propose: stay in plan-mode, add the work as a plan
		//     step, then execute it in plan-execute where bash is
		//     allowed (per-call y/N).
		// (2) mode switch: ask user to press Shift+Tab (cycles
		//     mode) or type /yolo (in-session toggle). Both keep
		//     the session, neither requires a restart.
		// (3) read alternative: maybe this question can be answered
		//     by reading files directly (the whitelisted inspector
		//     hint).
		//
		// Crucially, do NOT suggest --yolo flag restart. That loses
		// session state. Models that learned curl habits often
		// suggest it; the hint here exists to retrain that.
		return "no whitelist match. If you genuinely need this command: (1) call propose() with this work as a step — it will run in plan-execute where bash is allowed (per-call y/N); or (2) ask the user to press Shift+Tab to switch modes (cycles Ask → Plan → Yolo) or type /yolo to allow writes in-session — both keep your session intact, no restart needed; or (3) if the question can be answered by reading source, use read/grep/list_dir/git tool or a whitelisted inspector (go vet, go list, npm ls, …). NEVER suggest restarting with --yolo flag — that destroys session state and the user can switch modes without it."
	}
	return strings.Join(hints, "; ")
}

// hintCDPrefix matches a leading `cd ` or `cd\t`.
func hintCDPrefix(lower string) bool {
	return strings.HasPrefix(lower, "cd ") || strings.HasPrefix(lower, "cd\t")
}

// hintGitViaBash matches `git` as the first command token, or as the
// first token of a sub-command after a shell sequencing operator.
// The metachar separately makes the call illegal in plan-analyze, but
// the hint about using the git tool is the more useful nudge for the
// model — it converts a habit (`bash("git log")`) into a tool choice.
func hintGitViaBash(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	if fields[0] == "git" {
		return true
	}
	for i := 1; i < len(fields); i++ {
		if fields[i] != "git" {
			continue
		}
		switch fields[i-1] {
		case "&&", "||", ";", "|":
			return true
		}
	}
	return false
}

// hintGoTest matches `go test` as a token sequence — anywhere in the
// command, not just at the start. The model often writes `go test
// ./...` or chains it after a cd, and either form hits this branch.
func hintGoTest(lower string) bool {
	fields := strings.Fields(lower)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "go" && fields[i+1] == "test" {
			return true
		}
	}
	return false
}

// --- success-path advisory -------------------------------------------

// advisory classes still shipped. Each is a phrase the model sees, and
// firing one costs transcript bytes on every later request (the result
// is history). The set is deliberately small: a nudge that does not
// change behaviour is pure cost.
//
// MEASURED 2026-11-22 (40 sessions, 6,474 tool calls, ~/.seek/sessions/
// 202611*.jsonl). Two different controls, because "did the next call
// comply" alone is confounded by task locality:
//
//	A. next-tool-call compliance (within 2 calls) vs the no-hint base rate
//	B. within-session before/after: hinted behaviour over the 5 tool
//	   calls BEFORE each firing vs the 5 AFTER (same stretch of work)
//
//	class      fired   A: after hint / base   B: before → after
//	grep        1447      0.6% /  —             1.0% → 0.3%  (-0.7pp)
//	git          841      1.3% /  —             0.7% → 0.5%  (-0.2pp)
//	list_dir     291      0.0% /  —             0.7% → 0.1%  (-0.6pp)
//	read         255     13.3% / 12.7%          7.6% → 7.4%  (-0.2pp)
//	find          14      0.0% /  —              — (too few)
//	cd          1023     18.6% /  —            19.4% → 20.8% (+1.4pp)
//
// `read` is the tell: it "complies" at 13.3% — but the base rate of the
// next call being `read` when NO hint fired is 12.7%, so the hint moves
// the needle by 0.6pp. And control B is flat-negative for every class
// including read. The `cd` +1.4pp is inside the noise of a 5-call window.
//
// Conclusion: the success-path advisory does not teach. grep/git/
// list_dir/find are deleted outright — 2,593 of 3,871 firings (67%) for
// no measurable benefit, and the fingerprint of the whole mechanism is
// 410,967 bytes = 3.1% of all tool-result bytes, re-sent on every
// subsequent request. The two survivors (read, cd) are kept because the
// deletion of an entire teaching surface on one measurement is a bigger
// call than this change should make, and because `cd` was the ONLY class
// whose within-session delta was positive; both are now capped to one
// firing per class per session (one session measured 574 cd firings).
// If a future record still shows no effect for them, delete those too.
//
// The denial path above is untouched: a denied command MUST change, so
// its hint has a forced audience. That distinction — denial hint is
// load-bearing, success advisory is a nudge — is the whole lesson.

// bashAdvisory is the success-path nudge. Returns a phrase when the
// command hit one of the two remaining classes AND that class has not
// fired yet in this session; empty otherwise.
//
// limit may be nil (a Tool built without New, e.g. a bare Tool{} in a
// test): the guards still fire, just without the once-per-session cap.
func bashAdvisory(command string, limit *advisoryLimiter) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}

	var advisories []string

	// Pick the "effective" first token — if the command starts with
	// `cd <path> &&`, the meaningful command comes after.
	switch target := firstTokenAfterCD(fields); target {
	case "cat", "head", "tail":
		if limit.allow(advisoryRead) {
			advisories = append(advisories, "for reading file contents use the `read` tool — paginated output with line numbers, no shell quoting needed")
		}
	}

	// Orthogonal: cd-prefix waste. Only meaningful when the tool could
	// NOT strip the prefix automatically (Execute strips `cd <session
	// cwd> &&` before running, so the hint is reserved for the cds we
	// deliberately left alone).
	if hintCDPrefix(strings.ToLower(trimmed)) && limit.allow(advisoryCD) {
		advisories = append(advisories, "drop the `cd` prefix — seek's bash tool already runs from the project root")
	}

	if len(advisories) == 0 {
		return ""
	}
	return strings.Join(advisories, "; ")
}

// firstTokenAfterCD returns the meaningful first command token,
// skipping past `cd <path> &&` / `cd <path> ;`. Empty when nothing
// usable.
func firstTokenAfterCD(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	if fields[0] != "cd" {
		return fields[0]
	}
	for i := 1; i < len(fields); i++ {
		switch fields[i] {
		case "&&", "||", ";", "|":
			if i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	return ""
}
