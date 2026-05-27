// hint.go — context-aware error annotations for bash commands denied
// in plan-analyze. The base "permission denied" message from the
// permission package is generic; this layer inspects the actual
// command string and points the model at the specific fix.
//
// Why here and not in permission.go: the hint logic is bash-specific
// (it knows about `cd && X`, git-via-bash, `go test`, shell metachars
// — none of which the permission package should care about). The
// permission package returns a vanilla ErrDenied; bash.Execute wraps
// it with this hint at the tool boundary.
//
// Triggers ONLY for plan-analyze denials. Other workflows (Ask/Yolo
// with --no-yolo, outside-cwd, etc.) have their own deny reasons
// where these hints don't apply.

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
		return "no whitelist match. If you genuinely need this command: (1) call propose() with this work as a step — it will run in plan-execute where bash is allowed (per-call y/N); or (2) ask the user to press Shift+Tab to switch modes (cycles Ask → Yolo → Plan) or type /yolo to allow writes in-session — both keep your session intact, no restart needed; or (3) if the question can be answered by reading source, use read/grep/list_dir/git tool or a whitelisted inspector (go vet, go list, npm ls, …). NEVER suggest restarting with --yolo flag — that destroys session state and the user can switch modes without it."
	}
	return strings.Join(hints, "; ")
}

// hintCDPrefix matches a leading `cd ` or `cd\t`.
func hintCDPrefix(lower string) bool {
	return strings.HasPrefix(lower, "cd ") || strings.HasPrefix(lower, "cd\t")
}

// hintGitViaBash matches `git` as the first command token, or as the
// first token of a sub-command after a shell sequencing operator. The
// metachar separately makes the call illegal in plan-analyze, but the
// hint about using the git tool is the more useful nudge for the
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
