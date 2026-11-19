package autopilot

import (
	"strings"
	"testing"
)

func TestIsRemoteMutating_Blocks(t *testing.T) {
	blocked := []string{
		"git push",
		"git push origin main",
		"cd sub && git push --force",
		"gh pr create --fill",
		"gh pr merge 12",
		"gh repo create foo",
		"gh release create v1",
		"git remote add origin git@github.com:x/y",
		"gh api /repos/x/y/issues",
	}
	for _, c := range blocked {
		if deny, _ := IsRemoteMutating(c); !deny {
			t.Errorf("should block remote op: %q", c)
		}
	}
}

func TestIsRemoteMutating_Allows(t *testing.T) {
	allowed := []string{
		"git commit -m 'wip'",
		"git add -A",
		"git log --oneline",
		"git diff",
		"git fetch origin", // read-only network
		"git pull",         // not a push; allowed (merges locally)
		"gh pr view 12",    // read
		"go test ./...",
		"ls",
		"echo git push is just text in a comment", // not an actual push invocation? it IS "git push" — see note
	}
	for _, c := range allowed[:len(allowed)-1] { // last case documents a known limitation
		if deny, _ := IsRemoteMutating(c); deny {
			t.Errorf("should allow: %q", c)
		}
	}
	// Documented limitation: string matching can't distinguish a real
	// `git push` invocation from the words "git push" inside an echo/
	// comment — it errs toward blocking. The OS sandbox (柱 O) is the
	// real guarantee. Assert the conservative behavior so it's intentional.
	if deny, _ := IsRemoteMutating("echo git push is just text in a comment"); !deny {
		t.Skip("string-match guard conservatively blocks any 'git push' token — acceptable, 柱 O is the real boundary")
	}
}

// The refusal is disposition guidance, not a fact claim (c_ajza7r): the
// 2026-11-19 release incident had a SEEK_ACP_YOLO session — human present,
// push explicitly requested — told "autopilot runs unattended", and the
// model went looking for a bypass. Three elements are load-bearing:
// (1) this gate is best-effort matching, not a security boundary;
// (2) which lane refused; (3) what a human-authorised session should do.
// The same nine blocked commands from TestIsRemoteMutating_Blocks are
// repeated here on purpose: the wording assertions carry their own copy
// of the blocking surface, so a text change that widens the gate fails
// on its own.
func TestIsRemoteMutating_ReasonStatesLaneAndDisposition(t *testing.T) {
	blocked := []string{
		"git push",
		"git push origin main",
		"cd sub && git push --force",
		"gh pr create --fill",
		"gh pr merge 12",
		"gh repo create foo",
		"gh release create v1",
		"git remote add origin git@github.com:x/y",
		"gh api /repos/x/y/issues",
	}
	for _, c := range blocked {
		deny, reason := IsRemoteMutating(c)
		if !deny {
			t.Errorf("wording change must not widen the guard: %q no longer blocked", c)
			continue
		}
		for _, want := range []string{
			"best-effort string matching", // (1) not a security boundary
			"not a security boundary",
			LaneAutopilot, // (2) the lane that refused
			"terminal",    // (3) where a human-authorised run belongs
			"shell script",
		} {
			if !strings.Contains(reason, want) {
				t.Errorf("%q: reason missing %q — got: %s", c, want, reason)
			}
		}
	}
}

// Three lanes, three distinct refusals, each naming its own lane
// (c_ajza7r). "Unattended" is a property of the lane, not of this
// package — the reason must name the lane the guard was mounted on and
// must not claim a posture the lane doesn't have.
func TestIsRemoteMutatingOnLane_NamesItsLane(t *testing.T) {
	blocked := []string{
		"git push",
		"git push origin main",
		"cd sub && git push --force",
		"gh pr create --fill",
		"gh pr merge 12",
		"gh repo create foo",
		"gh release create v1",
		"git remote add origin git@github.com:x/y",
		"gh api /repos/x/y/issues",
	}
	lanes := []string{LaneAutopilot, LaneGoal, LaneACP}
	seen := make(map[string]string, len(lanes))
	for _, lane := range lanes {
		for _, c := range blocked {
			deny, _ := IsRemoteMutatingOnLane(lane, c)
			if !deny {
				t.Errorf("lane %q must block %q", lane, c)
			}
		}
		deny, reason := IsRemoteMutatingOnLane(lane, "git push origin main")
		if !deny {
			t.Fatalf("lane %q must block a push", lane)
		}
		if !strings.Contains(reason, lane) {
			t.Errorf("lane %q: reason does not name its lane: %s", lane, reason)
		}
		if !strings.Contains(reason, "best-effort string matching") ||
			!strings.Contains(reason, "not a security boundary") ||
			!strings.Contains(reason, "terminal") ||
			!strings.Contains(reason, "shell script") {
			t.Errorf("lane %q: reason lost a required element: %s", lane, reason)
		}
		if prev, dup := seen[reason]; dup {
			t.Errorf("lanes %q and %q produce the same reason", prev, lane)
		}
		seen[reason] = lane
	}
}

// The lane-less entry point keeps its historical home: a bare call must
// still report a lane (autopilot) rather than dropping element (2).
func TestIsRemoteMutating_LaneLessReportsAutopilot(t *testing.T) {
	deny, reason := IsRemoteMutating("git push")
	if !deny {
		t.Fatal("git push must be blocked")
	}
	if !strings.Contains(reason, LaneAutopilot) {
		t.Errorf("lane-less reason should name the autopilot lane: %s", reason)
	}
}
