package main

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/permission"
)

// autopilotInvocation resolves the `seek autopilot run "<goal>"` lane
// from the POSITIONAL arguments flag.Parse left behind (flag.Args()
// rather than os.Args[1]) — card c_skczdu.
//
// Why not os.Args[1]: flag.Parse stops at the first non-flag argument,
// so `seek autopilot run "g"` leaves ["autopilot","run","g"] behind
// either way, but `seek --yolo autopilot run "g"` only LOOKS like a
// flag-led invocation to an os.Args[1] check — it sees "--yolo" and
// concludes "not the autopilot lane", then falls through to the TUI.
// That shape is the one a human types to opt into yolo explicitly, so
// the lane detection must key off the same list flag.Parse produced.
//
// The second half of the same trap: `seek autopilot run "g" --yolo`
// can't work at all, because flag.Parse never re-enters after the
// positional — "--yolo" becomes part of the goal string (a real
// incident: the Report showed goal "noop gauge --yolo"). Rather than
// silently folding a flag into prose, return an error naming the shape
// that does work.
//
// lookup reports whether a name is a flag THIS binary registers
// (dash stripped). Production passes flag.CommandLine.Lookup; tests a
// stub. Only registered names count as a misplaced flag, so a goal that
// legitimately mentions "--foo" (e.g. "implement a --force switch") is
// still prose — the check must fire on the incident shape, not on any
// token that starts with a dash.
//
// Returns (isAutopilotLane, goal, error). A non-empty error means the
// invocation is malformed and the caller must fail loudly before any
// provider/session machinery starts.
func autopilotInvocation(args []string, lookup func(string) bool) (bool, string, error) {
	if len(args) == 0 || args[0] != "autopilot" {
		return false, "", nil
	}
	if len(args) < 2 || args[1] != "run" {
		// Not an error: `seek autopilot` alone reaches runAutopilot and
		// gets its usage line, same as before this change. A different
		// verb (`autopilot plan`) has no lane today either.
		return true, "", nil
	}
	rest := args[2:]
	for _, tok := range rest {
		if !misplacedFlag(tok, lookup) {
			continue
		}
		return true, "", fmt.Errorf(
			"seek autopilot run: %q is a flag, but flag parsing stops at the `autopilot` subcommand — put it BEFORE the subcommand: `seek %s autopilot run \"<goal>\"`", tok, tok)
	}
	return true, strings.TrimSpace(strings.Join(rest, " ")), nil
}

// misplacedFlag reports whether tok is a flag THIS binary registers:
// "--name" / "--name=value" / "-name" / "-name=value" whose name is in
// the flag set. An unknown dash-token stays prose (see the doc comment).
func misplacedFlag(tok string, lookup func(string) bool) bool {
	if lookup == nil || !strings.HasPrefix(tok, "-") {
		return false
	}
	name := strings.TrimPrefix(strings.TrimPrefix(tok, "-"), "-")
	if i := strings.Index(name, "="); i >= 0 {
		name = name[:i]
	}
	if name == "" {
		return false // bare "-" / "--" (the latter is flag.Parse's own terminator)
	}
	return lookup(name)
}

// prefInputs is the whole decision surface for the startup permission
// preference — every axis that can move it, in one value, so the
// precedence is testable without booting the CLI (c_skczdu).
type prefInputs struct {
	flagYolo      bool // --yolo
	acpYolo       bool // SEEK_ACP_YOLO, already gated on the acp lane
	acpLane       bool
	approvalStdin bool // --approval-stdin AND --json (already conjoined)
	autopilotLane bool // `seek autopilot run …`
}

// initialPreference resolves the startup preference. Precedence is
// Yolo > Ask > Deny:
//
//   - Yolo: --yolo, SEEK_ACP_YOLO on the acp lane, or the autopilot lane
//     (nobody is present to answer an ask). The no-remote bash guard,
//     not the preference, is what keeps an unattended run from pushing.
//   - Ask: the JSONL print lane with --approval-stdin (its prompts are
//     answerable over stdin) or a non-yolo acp session (its askFn turns
//     every request into session/request_permission, and every
//     no-answer ending denies — so Ask is never a loosening).
//   - Deny: everything else, including plain `-p`.
func initialPreference(in prefInputs) permission.Preference {
	switch {
	case in.flagYolo || in.acpYolo || in.autopilotLane:
		return permission.PrefYolo
	case in.approvalStdin || in.acpLane:
		return permission.PrefAsk
	default:
		return permission.PrefDeny
	}
}
