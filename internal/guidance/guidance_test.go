package guidance

// Behavioural tests for the deterministic rules. The failures that would
// actually hurt:
//
//  1. A rule fires on healthy work (read twelve different files) — a
//     false positive costs tokens on every later request.
//  2. A rule never fires on the shape it exists for — the feature is
//     decoration.
//  3. Rate limits don't hold: the measured lesson is that a nudge nobody
//     acts on, repeated, is the most expensive text in the transcript.
//  4. The same turn is advised twice (the loop can consult at two
//     boundaries in one iteration) — that halves every threshold.
//  5. A legitimate retry after an edit stays counted as a loop forever.

import (
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
)

// call builds one ToolBrief. fail=true stamps the error prefix the agent
// uses in tool-result content, so isFailure sees what production sees.
func call(name, args string, fail bool) agent.ToolBrief {
	b := agent.ToolBrief{Name: name, Args: args}
	if fail {
		b.Result = agent.ToolErrorPrefix + "exit status 1"
	}
	return b
}

func turn(idx int, textChars int, tools ...agent.ToolBrief) agent.TurnBrief {
	return agent.TurnBrief{Index: idx, Tools: tools, TextChars: textChars}
}

func TestQuietOnHealthyWork(t *testing.T) {
	t.Parallel()
	g := New()
	// Twelve different files, all succeeding, with prose: zero hints.
	for i := 0; i < 12; i++ {
		if got := g.Advise(turn(i, 40, call("read", `{"path":"file`+string(rune('a'+i))+`.go"}`, false))); got != "" {
			t.Fatalf("turn %d fired %q on healthy work", i, got)
		}
	}
}

func TestQuietOnTwoFailures(t *testing.T) {
	t.Parallel()
	g := New()
	// Two identical failures is a plausible retry; the rule starts at 3.
	// Prose keeps the silence rule out of the way.
	g.Advise(turn(0, 20, call("bash", `{"command":"go build ./..."}`, true)))
	if got := g.Advise(turn(1, 20, call("bash", `{"command":"go build ./..."}`, true))); got != "" {
		t.Fatalf("fired after only two failures: %q", got)
	}
}

func TestRetryLoopFiresAtThirdIdenticalFailure(t *testing.T) {
	t.Parallel()
	g := New()
	for i := 0; i < 2; i++ {
		g.Advise(turn(i, 20, call("bash", `{"command":"go build ./..."}`, true)))
	}
	got := g.Advise(turn(2, 20, call("bash", `{"command":"go build ./..."}`, true)))
	if got == "" {
		t.Fatal("retry-loop did not fire at the third identical failure")
	}
	if g.LastRule != RuleRetryLoop {
		t.Errorf("rule = %q, want %q", g.LastRule, RuleRetryLoop)
	}
	// The hint must name the call and say what to do instead — a bare
	// "you are looping" is not actionable.
	for _, want := range []string{"3 times", "go build", "change the arguments"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint missing %q:\n%s", want, got)
		}
	}
}

func TestRetryLoopIgnoresDifferentArgs(t *testing.T) {
	t.Parallel()
	g := New()
	// Same tool, three DIFFERENT failing targets: that is exploration,
	// not a loop. failure-run may fire (it keys on count, not identity),
	// but retry-loop must not.
	g.Advise(turn(0, 20, call("read", `{"path":"a.go"}`, true)))
	g.Advise(turn(1, 20, call("read", `{"path":"b.go"}`, true)))
	got := g.Advise(turn(2, 20, call("read", `{"path":"c.go"}`, true)))
	if strings.Contains(got, "exact call") {
		t.Errorf("retry-loop fired on distinct arguments:\n%s", got)
	}
}

func TestRetryLoopKeyIsCanonicalArgs(t *testing.T) {
	t.Parallel()
	g := New()
	// Key order and whitespace must not defeat identity — the same
	// equality key RepeatGuard and friction use.
	g.Advise(turn(0, 0, call("bash", `{"command":"ls","cwd":"/x"}`, true)))
	g.Advise(turn(1, 0, call("bash", `{ "cwd" : "/x" , "command" : "ls" }`, true)))
	got := g.Advise(turn(2, 0, call("bash", `{"cwd":"/x","command":"ls"}`, true)))
	if g.LastRule != RuleRetryLoop {
		t.Fatalf("canonicalisation did not unify the key: rule=%q text=%q", g.LastRule, got)
	}
}

func TestSuccessResetsTheRun(t *testing.T) {
	t.Parallel()
	g := New()
	// fail, fail, success, fail, fail → the run never reaches 3, and the
	// successful call must also clear that call's failure memory.
	// Prose on every turn isolates the failure-run counter from the
	// silence rule (a 4-turn silent streak would fire on its own).
	g.Advise(turn(0, 20, call("bash", `{"command":"go build"}`, true)))
	g.Advise(turn(1, 20, call("bash", `{"command":"go build"}`, true)))
	g.Advise(turn(2, 20, call("bash", `{"command":"go build"}`, false)))
	if got := g.Advise(turn(3, 20, call("bash", `{"command":"go build"}`, true))); got != "" {
		t.Errorf("run should have reset after a success, got %q", got)
	}
}

func TestLegitimateRetryAfterEditDoesNotAccumulate(t *testing.T) {
	t.Parallel()
	g := New()
	// The archetypal healthy shape: test fails, edit the file, test
	// again. Ten rounds of that must never look like a loop.
	for i := 0; i < 10; i++ {
		g.Advise(turn(i*2, 20, call("bash", `{"command":"go test ./pkg/x"}`, true)))
		g.Advise(turn(i*2+1, 20, call("edit", `{"path":"x.go"}`, false)))
	}
	if g.LastRule != "" {
		t.Errorf("edit-test cycle fired %q; a fix-then-retest loop is healthy work", g.LastText)
	}
}

func TestFailureRunAcrossDifferentTools(t *testing.T) {
	t.Parallel()
	g := New()
	g.Advise(turn(0, 10, call("read", `{"path":"a.go"}`, true)))
	g.Advise(turn(1, 10, call("grep", `{"pattern":"x"}`, true)))
	got := g.Advise(turn(2, 10, call("bash", `{"command":"go vet"}`, true)))
	if got == "" {
		t.Fatal("failure-run did not fire on three consecutive distinct failures")
	}
	if g.LastRule != RuleFailureRun {
		t.Errorf("rule = %q, want %q (retry-loop needs identical args)", g.LastRule, RuleFailureRun)
	}
}

func TestFailureRunResetsAfterFiring(t *testing.T) {
	t.Parallel()
	g := New()
	g.Advise(turn(0, 10, call("read", `{"path":"a"}`, true)))
	g.Advise(turn(1, 10, call("read", `{"path":"b"}`, true)))
	g.Advise(turn(2, 10, call("read", `{"path":"c"}`, true))) // fires
	// The run that fired has been reported; the next failure starts a
	// new count, so it must not re-qualify immediately.
	if got := g.Advise(turn(3, 10, call("read", `{"path":"d"}`, true))); got != "" {
		t.Errorf("run re-fired one turn later: %q", got)
	}
}

func TestIsFailureSeesBothShapes(t *testing.T) {
	t.Parallel()
	// Exec error.
	if !isFailure(agent.ToolBrief{Name: "bash", Err: errors.New("boom")}) {
		t.Error("exec error not treated as failure")
	}
	// Content-prefixed failure (what buildToolResultMsg produces).
	if !isFailure(agent.ToolBrief{Name: "bash", Result: agent.ToolErrorPrefix + "denied"}) {
		t.Error("error-prefixed result not treated as failure")
	}
	// A success that merely MENTIONS an error must not count.
	if isFailure(agent.ToolBrief{Name: "bash", Result: "the build printed: error: foo"}) {
		t.Error("a result mentioning an error was treated as a failure")
	}
}

func TestPreviewIsBounded(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 4000)
	got := preview(`{"p":"` + long + `"}`)
	if len(got) > 200 {
		t.Errorf("preview %d bytes, want ≤200 (the hint must not echo the payload)", len(got))
	}
}

func TestNilCallFailsMapSafeAfterNew(t *testing.T) {
	t.Parallel()
	// A zero-value Guide (not built via New) must not panic: callers
	// might embed it.
	var g Guide
	g.Advise(turn(0, 0, call("read", `{"path":"a"}`, true)))
}
