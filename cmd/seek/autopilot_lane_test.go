package main

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/autopilot"
	"github.com/whyiyhw/seek/internal/permission"
)

// fixtureLookup mimics flag.CommandLine.Lookup for the names the tests
// care about — the check only fires on flags this binary registers.
func fixtureLookup(names ...string) func(string) bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return func(n string) bool { return set[n] }
}

// TestAutopilotInvocation covers both flag shapes the card re-measured
// (c_skczdu) plus the shapes that must keep working:
//
//	seek autopilot run "g"           → lane, goal "g"      (unchanged)
//	seek autopilot run "g" --yolo    → clear error        (used to fold
//	                                 --yolo into the goal string; a real
//	                                 incident reported goal "noop gauge --yolo")
//	seek autopilot run "add --force" → lane, goal as typed (prose that
//	                                 happens to contain a dash token)
func TestAutopilotInvocation(t *testing.T) {
	lookup := fixtureLookup("yolo", "json", "model", "max-turns")

	cases := []struct {
		name     string
		args     []string
		wantLane bool
		wantGoal string
		wantErr  string // substring; "" = no error
	}{
		{
			name:     "plain run",
			args:     []string{"autopilot", "run", "fix the build"},
			wantLane: true,
			wantGoal: "fix the build",
		},
		{
			name:     "run with no goal reaches the usage path",
			args:     []string{"autopilot", "run"},
			wantLane: true,
		},
		{
			name:     "autopilot without a verb",
			args:     []string{"autopilot"},
			wantLane: true,
		},
		{
			name: "not the lane",
			args: []string{"-p", "hello"},
		},
		{
			name:     "unknown dash token inside a goal stays prose",
			args:     []string{"autopilot", "run", "implement a --force switch"},
			wantLane: true,
			wantGoal: "implement a --force switch",
		},
		{
			name:     "misplaced registered flag is rejected",
			args:     []string{"autopilot", "run", "fix the build", "--yolo"},
			wantLane: true,
			wantErr:  "put it BEFORE the subcommand",
		},
		{
			name:     "misplaced registered flag, value form",
			args:     []string{"autopilot", "run", "g", "--max-turns=5"},
			wantLane: true,
			wantErr:  "put it BEFORE the subcommand",
		},
		{
			name:     "multi-word goal joins with single spaces",
			args:     []string{"autopilot", "run", "  a  b  ", "c"},
			wantLane: true,
			wantGoal: "a  b   c",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotLane, gotGoal, err := autopilotInvocation(c.args, lookup)
			if gotLane != c.wantLane {
				t.Fatalf("lane = %v, want %v", gotLane, c.wantLane)
			}
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotGoal != c.wantGoal {
				t.Fatalf("goal = %q, want %q", gotGoal, c.wantGoal)
			}
		})
	}
}

// TestAutopilotInvocation_NoLookupMeansNoFlagCheck: a nil lookup (the
// defensive default) must not reject anything — the check only fires
// when it can positively identify a registered flag.
func TestAutopilotInvocation_NoLookupMeansNoFlagCheck(t *testing.T) {
	lane, goal, err := autopilotInvocation([]string{"autopilot", "run", "g", "--yolo"}, nil)
	if !lane || err != nil || goal != "g --yolo" {
		t.Fatalf("nil lookup: lane=%v goal=%q err=%v (nothing to compare against ⇒ prose)", lane, goal, err)
	}
}

// TestInitialPreference pins the whole Yolo > Ask > Deny precedence —
// including the new autopilot axis — as a table (c_skczdu acceptance ①).
func TestInitialPreference(t *testing.T) {
	cases := []struct {
		name string
		in   prefInputs
		want permission.Preference
	}{
		{"plain print mode", prefInputs{}, permission.PrefDeny},
		{"autopilot lane", prefInputs{autopilotLane: true}, permission.PrefYolo},
		{"autopilot lane with explicit --yolo", prefInputs{autopilotLane: true, flagYolo: true}, permission.PrefYolo},
		{"--yolo", prefInputs{flagYolo: true}, permission.PrefYolo},
		{"SEEK_ACP_YOLO", prefInputs{acpYolo: true, acpLane: true}, permission.PrefYolo},
		{"yolo beats approval-stdin ask", prefInputs{flagYolo: true, approvalStdin: true}, permission.PrefYolo},
		{"approval-stdin", prefInputs{approvalStdin: true}, permission.PrefAsk},
		{"non-yolo acp lane", prefInputs{acpLane: true}, permission.PrefAsk},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := initialPreference(c.in); got != c.want {
				t.Fatalf("initialPreference(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestAutopilotLane_PolicyYoloAndChildrenInherit is acceptance ①'s
// runtime half: the policy the lane actually builds is Yolo AND a
// subagent spawned from it inherits Yolo (Spawn is monotonic-only, so a
// child can never be stricter than its parent — which is exactly why the
// parent must start elevated).
func TestAutopilotLane_PolicyYoloAndChildrenInherit(t *testing.T) {
	dir := t.TempDir()
	p, err := permission.New(dir, initialPreference(prefInputs{autopilotLane: true}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Pref() != permission.PrefYolo {
		t.Fatalf("autopilot policy pref = %v, want yolo", p.Pref())
	}
	child, err := p.Spawn(dir, permission.Restriction{})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if child.Pref() != permission.PrefYolo {
		t.Fatalf("spawned child pref = %v, want yolo (children inherit)", child.Pref())
	}
}

// TestAutopilotLane_NoRemoteGuardStillDenies is acceptance ②, read from
// the side this change could break: elevating the pref must NOT loosen
// the no-remote guard. The guard keys off the lane label, so assert both
// halves — `git push` stays remote-mutating on that label, and the
// commands the fleet needs (go test) are not.
func TestAutopilotLane_NoRemoteGuardStillDenies(t *testing.T) {
	if autopilot.LaneAutopilot != "autopilot" {
		t.Fatalf("lane label = %q, want the literal %q the guard switches on", autopilot.LaneAutopilot, "autopilot")
	}
	if remote, _ := autopilot.IsRemoteMutatingOnLane(autopilot.LaneAutopilot, "git push origin main"); !remote {
		t.Fatal("git push must stay remote-mutating on the autopilot lane")
	}
	if remote, _ := autopilot.IsRemoteMutatingOnLane(autopilot.LaneAutopilot, "go test ./..."); remote {
		t.Fatal("go test must NOT be remote-mutating — that is the whole point of elevating the pref")
	}
}
