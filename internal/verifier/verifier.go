// Package verifier is the v8 柱 R R2 inner-loop judge: an independent
// session that decides whether a finished task actually satisfies its
// done_when assertions — by running the tests, reading the diff, and
// comparing against the task's conditions. It ONLY concludes; it never
// merges, installs, or flips the task's status (v8.md §5 "只下结论不合并").
//
// Shape (v8.md §5): a FRESH-context judge session — its only input is
// the evidence bundle assembled here; it cannot see the worker
// session's transcript or self-report, so a worker's "done" claim has
// no channel into the judgment.
//
// Anti-deception is MECHANICAL, not behavioural. The historical bug
// this package exists to catch (docs/pitfalls.md "Worktree-isolated
// subagent edited the MAIN tree", fix commit c21167d) had every
// soft signal green: the worker reported success, the report pointed
// at the worktree, and the test suite passed — only the diff's actual
// location was wrong. Therefore the deterministic layer (evidence.go)
// computes ground truth (test exit codes, changed paths, main-tree
// cleanliness) and the verdict composition CLAMPS the judge: a
// mechanical fail forces the overall verdict to fail no matter what
// the judge session says, and a judge "pass" that cites no resolvable
// evidence is downgraded to doubt. A verifier that can be talked into
// "pass" by green tests would faithfully reproduce the bug it was
// built to catch.
//
// Verdict domain: pass / fail / doubt — the three values the v8 R2
// card names for the inner loop. Butler's governance-layer four-value
// triage is deliberately NOT copied here: its ask_human escalation is
// a cross-system concern (seek's inner loop expresses it as doubt with
// a reason), and its lane-capability category has no meaning inside a
// single seek process. Doubt is the honest absorbent state — judge
// errors, unrunnable tests, assertion-less tasks all land there, so
// the §9.1 存疑率 metric can be recomputed from the Outcome fields.
//
// Configuration placement (v8.md §8 ❌ clause): the judge's system
// prompt is the compile-time constant in prompt.go. There is no file,
// env, or config.json read anywhere in this package that could change
// it — "core 写得到的地方" (the session's own filesystem reach:
// SEEK_HOME state, the repo working tree) is structurally unable to
// touch it. TestPrompt pins this behaviourally.
//
// Filesystem posture: the verifier writes NOTHING to disk. Its result
// rides the caller's in-memory Outcome into the report surface (v8 柱
// R R1 evidence fields). Persistence for human review is R3 (local
// inbox), not this card. Consequently the eval-lane hermeticity
// contract (scripts/eval-home-isolation-test.sh: every user-state
// write resolves through paths.Home(), run.sh pins SEEK_HOME) holds
// trivially — and under the unattended fake-key rule the judge call
// 401s and the verdict degrades to doubt, never to a fake pass.
package verifier

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Verdict values. Exactly three; see the package comment for why this
// is not butler's four-value set.
const (
	Pass  = "pass"
	Fail  = "fail"
	Doubt = "doubt"
)

// Request is one verification job. The caller (autopilot fleet) fills
// it from the task's own facts — never from the worker's self-report.
type Request struct {
	// TaskTitle labels the job in the verdict summary.
	TaskTitle string
	// Assertions are the task's done_when conditions, in order —
	// the same strings propose puts in a plan's "Done when" section
	// and the decomposer puts on each task.
	Assertions []string
	// WorkDir is the directory under judgment (the task's worktree):
	// test commands run here and the deliverable diff is read from
	// here.
	WorkDir string
	// Commit is the deliverable's short SHA inside WorkDir. Empty
	// means nothing was delivered (an empty diff is itself evidence
	// the judge must weigh).
	Commit string
	// TestCmds are run verbatim in WorkDir by the deterministic
	// layer, each bounded by TestTimeout. The caller derives them
	// from the task (e.g. "go test ./..." when a go.mod is present),
	// NOT from the worker's claims.
	TestCmds []string
	// TestTimeout bounds each test command. Zero = 10 minutes.
	TestTimeout time.Duration
	// ScopeRoot, when non-empty, is the main repo tree the task was
	// forbidden to touch (worktree isolation). The scope check
	// compares ScopeBaseline to the tree's current state.
	ScopeRoot string
	// ScopeBaseline is the pre-run snapshot of ScopeRoot's dirt
	// (MainTreeSnapshot output). Empty baseline = the tree was clean
	// when the task started.
	ScopeBaseline string
}

// TestRun is one deterministic test-command execution.
type TestRun struct {
	ID       string // "T1", "T2", …
	Cmd      string
	ExitCode int
	Output   string // capped head+tail
	Duration time.Duration
	TimedOut bool
	RunErr   string // exec failure (binary missing, …); empty on a real run
	Evidence string // rendered evidence block shown to the judge
}

// DiffFootprint is the deterministic diff evidence for the deliverable.
type DiffFootprint struct {
	Commit       string
	Numstat      string // git show --numstat --format= output
	Patch        string // capped unified patch
	ChangedPaths []string
	Empty        bool   // no commit or no changed paths
	Evidence     string // rendered evidence block shown to the judge
}

// ScopeCheck is the deterministic main-tree isolation check.
type ScopeCheck struct {
	Root     string
	Violated bool
	Before   string
	After    string
	Delta    string // the paths that appeared/changed since baseline
	Evidence string // rendered evidence block shown to the judge
}

// AssertionResult is the per-assertion verdict row: which assertion,
// what it was checked against, and the conclusion. Rows exist for
// EVERY assertion — including ones the judge skipped (those are
// filled in as doubt), so a verdict is never a prose blob.
type AssertionResult struct {
	Index    int // 1-based, matches Request.Assertions order
	Text     string
	Verdict  string   // pass | fail | doubt
	Evidence []string // evidence ids cited (T1, D, S, …); grounded ids only
	Note     string   // one-line reasoning from the judge, or the clamp reason
}

// InvariantResult is a deterministic check the judge cannot override.
type InvariantResult struct {
	ID      string // "S" (scope), "T" (tests)
	Name    string
	Verdict string // pass | fail | doubt
	Detail  string
}

// Verdict is the full judgment record. It lands on the autopilot
// Outcome (v8 柱 R R1 evidence-field surface) so the §9.1 metrics —
// 存疑率 (doubt share) and per-task review latency — stay computable.
type Verdict struct {
	Overall     string // pass | fail | doubt
	Assertions  []AssertionResult
	Invariants  []InvariantResult
	Tests       []TestRun
	Diff        DiffFootprint
	Scope       *ScopeCheck // nil when no ScopeRoot was given
	JudgeNote   string      // judge's overall remark
	JudgeError  string      // judge session failure; non-empty forces doubt
	Model       string
	JudgeTokens int // judge call token burn (prompt+completion)
	StartedAt   time.Time
	FinishedAt  time.Time
}

// Summary renders the one-line verdict for the report body:
// "verdict=fail" plus the first deciding cause.
func (v *Verdict) Summary() string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("verdict=")
	b.WriteString(v.Overall)
	for _, inv := range v.Invariants {
		if inv.Verdict == Fail {
			b.WriteString(" (")
			b.WriteString(inv.Name)
			b.WriteString(")")
			break
		}
	}
	if v.Overall == Fail && !anyInvariant(v.Invariants, Fail) {
		// A failing assertion with no failing invariant.
		for _, a := range v.Assertions {
			if a.Verdict == Fail {
				fmt.Fprintf(&b, " (assertion %d)", a.Index)
				break
			}
		}
	}
	return b.String()
}

func anyInvariant(invs []InvariantResult, verdict string) bool {
	for _, inv := range invs {
		if inv.Verdict == verdict {
			return true
		}
	}
	return false
}

// Verifier runs verification jobs. Construct with New (production
// DeepSeek judge) or NewWithJudge (tests). It holds no mutable state,
// so one Verifier can serve concurrent tasks safely.
type Verifier struct {
	judge Judge
	model string
}

// New builds the production verifier: the judge session is a single
// fresh-context DeepSeek chat call (system prompt = compile-time
// constant, no runtime override path).
func New(client JudgeClient, model string) *Verifier {
	return NewWithJudge(&DeepSeekJudge{Client: client}, model)
}

// NewWithJudge builds a verifier around an injected judge — the seam
// every test in this package uses. The judge receives ONLY the
// evidence bundle; there is no parameter that could smuggle in a
// prompt override or worker-session content.
func NewWithJudge(j Judge, model string) *Verifier {
	return &Verifier{judge: j, model: model}
}

// Verify runs one job: deterministic evidence → judge session →
// clamped composition. It never returns an error verdict-wise: a
// judge failure, a ctx cancellation, or an unusable bundle degrades
// to doubt with the reason recorded — the inner loop's failure
// posture is "unproven", never "failed" and never "passed".
func (ver *Verifier) Verify(ctx context.Context, req Request) *Verdict {
	started := time.Now()
	vd := &Verdict{Model: ver.model, StartedAt: started}
	defer func() { vd.FinishedAt = time.Now() }()

	// Deterministic evidence first — ground truth before any model
	// opinion exists.
	vd.Tests = runTests(ctx, req)
	vd.Diff = collectDiff(ctx, req)
	if req.ScopeRoot != "" {
		sc := checkScope(ctx, req)
		vd.Scope = &sc
	}

	// Mechanical invariants — the judge cannot talk its way past these.
	vd.Invariants = evaluateInvariants(req, vd)

	// Judge session (fresh context; its only input is the bundle).
	known := knownEvidenceIDs(vd) // evidence ids the judge may cite
	j, jerr := ver.judge.Judge(ctx, ver.model, buildBundle(req, vd))
	jerrStr := ""
	if jerr != nil {
		jerrStr = jerr.Error()
	}
	vd.JudgeError = jerrStr
	vd.JudgeNote = j.Note
	vd.JudgeTokens = j.Tokens
	vd.Assertions = clampAssertions(req.Assertions, j.Assertions, known, jerrStr)

	vd.Overall = composeOverall(vd)
	return vd
}

// knownEvidenceIDs lists the evidence ids grounded in the mechanical
// bundle — "T1"… per test run, "D" for the deliverable diff, "S" for
// the scope check. A judge citation outside this set is not evidence.
func knownEvidenceIDs(vd *Verdict) map[string]bool {
	m := make(map[string]bool, len(vd.Tests)+2)
	for _, t := range vd.Tests {
		m[t.ID] = true
	}
	m["D"] = true
	if vd.Scope != nil {
		m["S"] = true
	}
	return m
}

// evaluateInvariants derives the deterministic verdict rows. These
// encode the historical bug's signature: green tests prove nothing
// about WHERE the change landed; the scope check does.
func evaluateInvariants(req Request, vd *Verdict) []InvariantResult {
	var invs []InvariantResult

	if vd.Scope != nil {
		inv := InvariantResult{ID: "S", Name: "scope", Verdict: Pass,
			Detail: "main tree unchanged since baseline"}
		if vd.Scope.Violated {
			inv.Verdict = Fail
			inv.Detail = "main tree changed during the task (worktree isolation bypassed): " + vd.Scope.Delta
		}
		invs = append(invs, inv)
	}

	if len(req.TestCmds) > 0 {
		inv := InvariantResult{ID: "T", Name: "tests", Verdict: Pass,
			Detail: "all provided test commands exited 0"}
		if len(vd.Tests) < len(req.TestCmds) {
			// Commands were requested but the layer couldn't run them
			// (e.g. no WorkDir) — an unverified suite is doubt, never
			// a silent pass.
			inv.Verdict = Doubt
			inv.Detail = fmt.Sprintf("only %d of %d requested test commands produced a result", len(vd.Tests), len(req.TestCmds))
		}
		for _, t := range vd.Tests {
			if t.RunErr != "" || t.TimedOut {
				inv.Verdict = Doubt
				inv.Detail = t.ID + " (" + t.Cmd + ") did not produce a usable result: " + runProblem(t)
				break
			}
			if t.ExitCode != 0 {
				inv.Verdict = Fail
				inv.Detail = fmt.Sprintf("%s (%s) exited %d", t.ID, t.Cmd, t.ExitCode)
				break
			}
		}
		invs = append(invs, inv)
	}

	if len(req.Assertions) == 0 {
		invs = append(invs, InvariantResult{ID: "A", Name: "assertions",
			Verdict: Doubt,
			Detail:  "task carried no done_when assertions — nothing checkable to verify against"})
	}
	return invs
}

// clampAssertions merges the judge's per-assertion rows with the
// mechanical rules. Every assertion gets a row; judge omissions,
// unparseable output, and unevidenced passes all collapse to doubt —
// the composition then decides the overall verdict.
func clampAssertions(assertions []string, judged []JudgedAssertion, known map[string]bool, jerr string) []AssertionResult {
	byIndex := make(map[int]JudgedAssertion, len(judged))
	for _, j := range judged {
		byIndex[j.Index] = j
	}
	out := make([]AssertionResult, len(assertions))
	for i, text := range assertions {
		r := AssertionResult{Index: i + 1, Text: text, Verdict: Doubt}
		j, ok := byIndex[i+1]
		if jerr != "" {
			r.Note = "judge session failed: " + jerr
			out[i] = r
			continue
		}
		if !ok {
			r.Note = "judge did not address this assertion"
			out[i] = r
			continue
		}
		r.Note = j.Note
		// Ground every citation: unevidenced ids are dropped, and a
		// "pass" left with zero grounded citations is downgraded to
		// doubt — a pass must point at something real.
		for _, id := range j.Evidence {
			if known[id] {
				r.Evidence = append(r.Evidence, id)
			}
		}
		switch j.Verdict {
		case Pass:
			if len(r.Evidence) == 0 {
				r.Verdict = Doubt
				r.Note = strings.TrimSpace("no grounded evidence cited — " + r.Note)
			} else {
				r.Verdict = Pass
			}
		case Fail:
			r.Verdict = Fail
		default:
			r.Verdict = Doubt
		}
		out[i] = r
	}
	return out
}

// composeOverall folds rows into the single verdict. fail beats doubt
// beats pass; a judge error floors the result at doubt. This is the
// clamp: no arrangement of judge rows can lift a mechanical fail.
func composeOverall(vd *Verdict) string {
	overall := Pass
	degrade := func(to string) {
		if to == Fail || (to == Doubt && overall == Pass) {
			overall = to
		}
	}
	for _, inv := range vd.Invariants {
		degrade(inv.Verdict)
	}
	for _, a := range vd.Assertions {
		degrade(a.Verdict)
	}
	if vd.JudgeError != "" {
		degrade(Doubt)
	}
	return overall
}

func runProblem(t TestRun) string {
	if t.TimedOut {
		return "timed out"
	}
	return t.RunErr
}
