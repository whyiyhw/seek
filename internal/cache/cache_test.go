package cache

import (
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// recordFlash is shorthand for the most common call shape in these
// tests — V4-Flash, standard tier. Tests that care about cost lock-in
// across model/tier transitions construct their own Record calls.
func recordFlash(tr *Tracker, u deepseek.Usage) {
	tr.Record(u, deepseek.ModelV4Flash, pricing.TierStandard)
}

func TestTracker_Empty(t *testing.T) {
	tr := New()
	if got := tr.Cumulative(); got.TotalTokens != 0 {
		t.Errorf("Cumulative on empty = %+v", got)
	}
	if got := tr.HitRatio(); got != 0 {
		t.Errorf("HitRatio on empty = %v", got)
	}
	if got := tr.CumulativeCost(); got != 0 {
		t.Errorf("CumulativeCost on empty = %v", got)
	}
	if !strings.Contains(tr.Summary(), "no turns") {
		t.Errorf("Summary on empty = %q", tr.Summary())
	}
}

func TestTracker_CumulativeAcrossTurns(t *testing.T) {
	tr := New()
	recordFlash(tr, deepseek.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, PromptCacheHitTokens: 0, PromptCacheMissTokens: 100})
	recordFlash(tr, deepseek.Usage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230, PromptCacheHitTokens: 150, PromptCacheMissTokens: 50})
	recordFlash(tr, deepseek.Usage{PromptTokens: 200, CompletionTokens: 25, TotalTokens: 225, PromptCacheHitTokens: 180, PromptCacheMissTokens: 20})

	c := tr.Cumulative()
	if c.PromptTokens != 500 {
		t.Errorf("PromptTokens = %d, want 500", c.PromptTokens)
	}
	if c.CompletionTokens != 75 {
		t.Errorf("CompletionTokens = %d, want 75", c.CompletionTokens)
	}
	if c.PromptCacheHitTokens != 330 || c.PromptCacheMissTokens != 170 {
		t.Errorf("cache split = (%d, %d), want (330, 170)", c.PromptCacheHitTokens, c.PromptCacheMissTokens)
	}
	// Hit ratio = 330 / (330 + 170) = 0.66
	if got := tr.HitRatio(); got < 0.659 || got > 0.661 {
		t.Errorf("HitRatio = %v, want ≈0.66", got)
	}
	if got := tr.SavedTokens(); got != 330 {
		t.Errorf("SavedTokens = %d, want 330", got)
	}
}

func TestTracker_Last(t *testing.T) {
	tr := New()
	// Empty tracker returns zero usage.
	if got := tr.Last(); got.PromptTokens != 0 {
		t.Errorf("Last() on empty = %+v, want zero", got)
	}
	recordFlash(tr, deepseek.Usage{PromptTokens: 100})
	recordFlash(tr, deepseek.Usage{PromptTokens: 200})
	recordFlash(tr, deepseek.Usage{PromptTokens: 50})
	// Last() returns the most recent turn, NOT the cumulative.
	if got := tr.Last().PromptTokens; got != 50 {
		t.Errorf("Last().PromptTokens = %d, want 50", got)
	}
	// Cumulative should still be 350.
	if got := tr.Cumulative().PromptTokens; got != 350 {
		t.Errorf("Cumulative().PromptTokens = %d, want 350", got)
	}
}

// TestTracker_SetBase_LastIgnoresBase locks in the load-bearing invariant
// for the resume ctx% fix: SetBase must contribute to Cumulative() but NOT
// to Last(). Without this, a resumed session's aggregate usage makes the
// status bar show "ctx 211%" on start.
func TestTracker_SetBase_LastIgnoresBase(t *testing.T) {
	tr := New()

	// Set a base representing a prior session with heavy usage.
	base := deepseek.Usage{PromptTokens: 500_000, CompletionTokens: 20_000, TotalTokens: 520_000}
	tr.SetBase(base, "deepseek-v4-flash", pricing.TierStandard)

	// Last() must return zero — no turns recorded yet.
	if got := tr.Last(); got.PromptTokens != 0 {
		t.Errorf("Last() after SetBase = %+v, want zero (no turns yet)", got)
	}
	if got := tr.LastCost(); got != 0 {
		t.Errorf("LastCost() after SetBase = %v, want 0", got)
	}

	// Cumulative() must include the base.
	if got := tr.Cumulative().PromptTokens; got != 500_000 {
		t.Errorf("Cumulative().PromptTokens after SetBase = %d, want 500_000", got)
	}

	// Record one genuine turn — Last() must return the turn, not the base.
	recordFlash(tr, deepseek.Usage{PromptTokens: 10_000, CompletionTokens: 1_000, TotalTokens: 11_000})
	if got := tr.Last().PromptTokens; got != 10_000 {
		t.Errorf("Last().PromptTokens after record = %d, want 10_000 (the turn, not the base)", got)
	}
	if got := tr.Cumulative().PromptTokens; got != 510_000 {
		t.Errorf("Cumulative().PromptTokens after record = %d, want 510_000 = base(500k) + turn(10k)", got)
	}
}

// TestTracker_SetBase_EmptyUsageIsNoOp verifies that SetBase with a zero
// Usage does not pollute Cumulative or Last.
func TestTracker_SetBase_EmptyUsageIsNoOp(t *testing.T) {
	tr := New()
	tr.SetBase(deepseek.Usage{}, "deepseek-v4-flash", pricing.TierStandard)
	if got := tr.Cumulative().TotalTokens; got != 0 {
		t.Errorf("Cumulative() with empty SetBase = %+v, want zero", got)
	}
	if got := tr.Last().TotalTokens; got != 0 {
		t.Errorf("Last() with empty SetBase = %+v, want zero", got)
	}
}

func TestTracker_TurnsReturnsCopy(t *testing.T) {
	tr := New()
	recordFlash(tr, deepseek.Usage{PromptTokens: 1})
	turns := tr.Turns()
	turns[0].PromptTokens = 999 // mutate the snapshot
	if got := tr.Cumulative().PromptTokens; got != 1 {
		t.Errorf("internal state mutated via Turns() snapshot: %d", got)
	}
}

func TestTracker_SummaryFormat(t *testing.T) {
	tr := New()
	recordFlash(tr, deepseek.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, PromptCacheHitTokens: 50, PromptCacheMissTokens: 50})
	s := tr.Summary()
	// Expect a single line containing key fields. Loose checks so cosmetic
	// formatting tweaks don't break the test.
	for _, frag := range []string{"1 turn", "hit 50", "miss 50", "50.0%"} {
		if !strings.Contains(s, frag) {
			t.Errorf("Summary missing %q: %q", frag, s)
		}
	}
}

func TestTracker_ConcurrentSafe(t *testing.T) {
	tr := New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordFlash(tr, deepseek.Usage{PromptTokens: 1, PromptCacheHitTokens: 1})
		}()
	}
	wg.Wait()
	if c := tr.Cumulative(); c.PromptTokens != 100 || c.PromptCacheHitTokens != 100 {
		t.Errorf("race-lost writes: %+v", c)
	}
}

// TestTracker_CumulativeCostLockedInAtRecord is the load-bearing pin
// for the cross-model cost bug. Switching from V4-Flash to V4-Pro
// mid-session must NOT retroactively re-price the V4-Flash turn at
// V4-Pro's ~3.1× rate, and switching back must not re-price the
// V4-Pro turn at the cheaper V4-Flash rate either.
func TestTracker_CumulativeCostLockedInAtRecord(t *testing.T) {
	tr := New()

	// Turn 1 — 1M completion @ V4-Flash standard ($0.28/M output) = $0.28.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierStandard,
	)
	if got := tr.CumulativeCost(); got < 0.279 || got > 0.281 {
		t.Errorf("after V4-Flash turn: cost = %v, want ≈0.28", got)
	}

	// Turn 2 — 1M completion @ V4-Pro standard ($0.87/M output) = $0.87.
	// Cumulative now = 0.28 + 0.87 = 1.15.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Pro, pricing.TierStandard,
	)
	if got := tr.CumulativeCost(); got < 1.149 || got > 1.151 {
		t.Errorf("after V4-Pro turn: cost = %v, want ≈1.15", got)
	}

	// Turn 3 — 1M completion @ V4-Flash standard ($0.28/M) = $0.28.
	// Cumulative now = 1.15 + 0.28 = 1.43. If we were re-pricing all
	// turns at the CURRENT model (V4-Flash) this would come out to
	// 3 × 0.28 = 0.84 instead — the exact bug this pins against.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierStandard,
	)
	if got := tr.CumulativeCost(); got < 1.429 || got > 1.431 {
		t.Errorf("after switch-back to V4-Flash: cost = %v, want ≈1.43 (NOT 0.84 — that would mean we re-priced)", got)
	}
}

// TestTracker_AdoptChild_AggregatesUsageAndCost pins the v5 柱 G cost
// rollup contract: a parent's Cumulative()/CumulativeCost() includes
// every adopted child's totals so the status bar shows
// parent + Σ children.
func TestTracker_AdoptChild_AggregatesUsageAndCost(t *testing.T) {
	parent := New()
	recordFlash(parent, deepseek.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, PromptCacheHitTokens: 80, PromptCacheMissTokens: 20})

	childA := New()
	recordFlash(childA, deepseek.Usage{PromptTokens: 200, CompletionTokens: 30, TotalTokens: 230, PromptCacheHitTokens: 150, PromptCacheMissTokens: 50})
	childB := New()
	recordFlash(childB, deepseek.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60, PromptCacheHitTokens: 0, PromptCacheMissTokens: 50})

	parent.AdoptChild(childA)
	parent.AdoptChild(childB)

	c := parent.Cumulative()
	if c.PromptTokens != 350 { // 100 + 200 + 50
		t.Errorf("PromptTokens = %d, want 350 (parent 100 + childA 200 + childB 50)", c.PromptTokens)
	}
	if c.CompletionTokens != 60 { // 20 + 30 + 10
		t.Errorf("CompletionTokens = %d, want 60", c.CompletionTokens)
	}
	if c.PromptCacheHitTokens != 230 || c.PromptCacheMissTokens != 120 {
		t.Errorf("cache split = (%d, %d), want (230, 120)", c.PromptCacheHitTokens, c.PromptCacheMissTokens)
	}

	// Cost rollup must walk children too. Each turn is 1 prompt + 1 completion
	// at V4-Flash standard rates; exact dollar values don't matter for this
	// assertion as long as parent ≥ each child's cost.
	pCost := parent.CumulativeCost()
	aCost := childA.CumulativeCost()
	bCost := childB.CumulativeCost()
	wantCost := childOnlyCost(parent) + aCost + bCost
	if pCost < wantCost-1e-9 || pCost > wantCost+1e-9 {
		t.Errorf("CumulativeCost = %v, want %v (parent-own %v + childA %v + childB %v)", pCost, wantCost, childOnlyCost(parent), aCost, bCost)
	}

	// Last() / LastCost() are parent-only by contract — children's most-recent
	// turn must NOT bleed in.
	if got := parent.Last().PromptTokens; got != 100 {
		t.Errorf("Last() leaked from child: PromptTokens = %d, want 100 (parent's own turn)", got)
	}
}

// childOnlyCost returns the parent's own-turns cost, excluding adopted
// children. Used by tests to construct expectations without rebuilding
// the pricing maths.
func childOnlyCost(t *Tracker) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	sum := t.baseCost
	for _, r := range t.turns {
		sum += r.Cost
	}
	return sum
}

// TestTracker_AdoptChild_IsIdempotent: spawn-retry paths re-call
// AdoptChild with the same *Tracker; the second call must be a no-op
// rather than double-count.
func TestTracker_AdoptChild_IsIdempotent(t *testing.T) {
	parent := New()
	child := New()
	recordFlash(child, deepseek.Usage{PromptTokens: 100, TotalTokens: 100})

	parent.AdoptChild(child)
	parent.AdoptChild(child)
	parent.AdoptChild(child)

	if got := parent.Cumulative().PromptTokens; got != 100 {
		t.Errorf("repeated AdoptChild double-counted: PromptTokens = %d, want 100", got)
	}
}

// TestTracker_AdoptChild_NilAndSelfAreNoOp: defensive guards against
// nil receivers / self-adoption.
func TestTracker_AdoptChild_NilAndSelfAreNoOp(t *testing.T) {
	parent := New()
	parent.AdoptChild(nil)  // must not panic
	parent.AdoptChild(parent) // must not panic, must not adopt self
	// Adopting self would cause infinite recursion in Cumulative — verify
	// we can still walk without stack-overflow.
	_ = parent.Cumulative()

	var nilT *Tracker
	nilT.AdoptChild(New()) // nil receiver: must not panic
}

// TestTracker_AdoptChild_NestedPanics defends spawn-depth = 1 (v5 §2):
// AdoptChild on a Tracker that already has children panics so the bug
// surfaces at the orchestrator boundary rather than silently
// double-counting via transitive walk.
func TestTracker_AdoptChild_NestedPanics(t *testing.T) {
	grand := New()
	child := New()
	parent := New()

	child.AdoptChild(grand) // child now has children of its own
	defer func() {
		if r := recover(); r == nil {
			t.Error("AdoptChild on a Tracker with existing children must panic")
		}
	}()
	parent.AdoptChild(child) // should panic
}

// TestTracker_AdoptChild_ConcurrentSpawn: spawning N parallel
// subagents (each adopting + recording in its own goroutine) must
// aggregate every child's tokens without losing writes.
func TestTracker_AdoptChild_ConcurrentSpawn(t *testing.T) {
	parent := New()
	var wg sync.WaitGroup
	const N = 50
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child := New()
			recordFlash(child, deepseek.Usage{PromptTokens: 10, PromptCacheHitTokens: 5, PromptCacheMissTokens: 5})
			parent.AdoptChild(child)
		}()
	}
	wg.Wait()

	c := parent.Cumulative()
	if c.PromptTokens != N*10 {
		t.Errorf("PromptTokens after %d parallel adoptions = %d, want %d", N, c.PromptTokens, N*10)
	}
	if c.PromptCacheHitTokens != N*5 {
		t.Errorf("PromptCacheHitTokens = %d, want %d", c.PromptCacheHitTokens, N*5)
	}
}

// TestTracker_AdoptChild_ConcurrentCumulativeDoesNotDeadlock: parent
// Cumulative running while another goroutine calls AdoptChild + child
// Record must not deadlock. Cumulative's snapshot-then-release pattern
// is the load-bearing invariant; without it, holding parent.mu across
// child.Cumulative() (which takes child.mu) could compose into a lock
// ordering hazard if any future code path takes the locks in reverse.
func TestTracker_AdoptChild_ConcurrentCumulativeDoesNotDeadlock(t *testing.T) {
	parent := New()
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Reader: hammer Cumulative on parent.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = parent.Cumulative()
				_ = parent.CumulativeCost()
			}
		}
	}()

	// Writer: spawn children, record into them, adopt.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			child := New()
			recordFlash(child, deepseek.Usage{PromptTokens: 1})
			parent.AdoptChild(child)
		}
	}()

	// Let it run briefly then stop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			recordFlash(parent, deepseek.Usage{PromptTokens: 1})
		}
		close(stop)
	}()

	wg.Wait()
}

// TestTracker_TierTransitionDoesNotRePrice covers the off-peak
// boundary case — same root cause as the cross-model bug, different
// trigger.
func TestTracker_TierTransitionDoesNotRePrice(t *testing.T) {
	tr := New()
	// Standard tier: 1M completion @ V4-Flash = $0.28.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierStandard,
	)
	// Off-peak tier (50% discount): 1M completion = $0.14.
	tr.Record(
		deepseek.Usage{CompletionTokens: 1_000_000},
		deepseek.ModelV4Flash, pricing.TierOffPeak,
	)
	// Expected = 0.28 + 0.14 = 0.42. NOT 0.28 (both at off-peak)
	// and NOT 0.56 (both at standard).
	if got := tr.CumulativeCost(); got < 0.419 || got > 0.421 {
		t.Errorf("CumulativeCost across tier boundary = %v, want ≈0.42 (each turn priced at its own tier)", got)
	}
}

// TestTracker_AdoptChild_DoubleCountAfterResumePanics is the load-bearing
// G2 pin: when the parent has been resumed (SetBase'd), its baseUsage
// already aggregates every prior-session child's cumulative usage.
// Adopting a child that itself carries prior turns at this point would
// silently double-count via the children-walk in Cumulative — a v5 柱 G
// risk explicitly called out in feature-subagent.md §8.
//
// The guard panics rather than returns an error because, like the
// "nested children" panic on the same function, it represents an
// orchestrator bug that should fail loudly at the boundary rather than
// quietly produce mis-priced status-bar numbers.
func TestTracker_AdoptChild_DoubleCountAfterResumePanics(t *testing.T) {
	parent := New()
	// Simulate a resumed session: prior cumulative already includes
	// whatever children ran in session N-1.
	parent.SetBase(deepseek.Usage{
		PromptTokens: 1000, CompletionTokens: 100, TotalTokens: 1100,
		PromptCacheHitTokens: 800, PromptCacheMissTokens: 200,
	}, deepseek.ModelV4Flash, pricing.TierStandard)

	// Simulate a child Tracker that has been restored from disk
	// (or, more likely in practice, a buggy call site that recycles
	// a child that's already been Recorded into).
	child := New()
	recordFlash(child, deepseek.Usage{PromptTokens: 200, TotalTokens: 200})

	defer func() {
		if r := recover(); r == nil {
			t.Error("AdoptChild on a non-fresh child after parent SetBase must panic — double-count risk not guarded")
		}
	}()
	parent.AdoptChild(child) // expected to panic
}

// TestTracker_AdoptChild_DoubleCountGuardCoversChildBase covers the
// "child also has baseUsage" branch of the G2 guard: even if the child
// has zero Recorded turns, a non-zero baseUsage represents prior
// accounting that the parent's baseUsage already encompasses.
func TestTracker_AdoptChild_DoubleCountGuardCoversChildBase(t *testing.T) {
	parent := New()
	parent.SetBase(deepseek.Usage{TotalTokens: 1000}, deepseek.ModelV4Flash, pricing.TierStandard)

	// Child has SetBase but no Record — still a "non-fresh" child
	// per the G2 contract.
	child := New()
	child.SetBase(deepseek.Usage{TotalTokens: 50}, deepseek.ModelV4Flash, pricing.TierStandard)

	defer func() {
		if r := recover(); r == nil {
			t.Error("AdoptChild on a child with baseUsage after parent SetBase must panic")
		}
	}()
	parent.AdoptChild(child)
}

// TestTracker_AdoptChild_FreshChildAfterResumeOK is the inverse pin:
// the G2 guard MUST NOT fire on the production happy path. After a
// parent resumes via SetBase, every new Spawn creates a fresh child
// Tracker (cache.New() — zero turns, zero baseUsage); adopting it is
// the documented, correct flow and must succeed.
func TestTracker_AdoptChild_FreshChildAfterResumeOK(t *testing.T) {
	parent := New()
	parent.SetBase(deepseek.Usage{TotalTokens: 1000}, deepseek.ModelV4Flash, pricing.TierStandard)

	child := New() // fresh — what subagent.Manager.Spawn always passes
	parent.AdoptChild(child)
	// Subagent then records into its own Tracker. This is the normal
	// post-resume spawn flow.
	recordFlash(child, deepseek.Usage{PromptTokens: 50, TotalTokens: 50})

	// Parent's Cumulative correctly stacks: base (1000) + child (50)
	// = 1050. No double-count, no panic.
	if got := parent.Cumulative().TotalTokens; got != 1050 {
		t.Errorf("Cumulative after fresh-adopt-after-resume = %d, want 1050 (base 1000 + child 50)", got)
	}
}

// TestTracker_AdoptChild_NonFreshChildWithoutResumeOK confirms the
// guard is correctly gated on hasBase: in a fresh (non-resumed) parent,
// adopting a child that already has its own turns is permitted because
// there's no overlap with parent's baseUsage (there is none). This is
// what TestTracker_AdoptChild_IsIdempotent has always relied on; we're
// pinning it explicitly so the gating intent is documented.
func TestTracker_AdoptChild_NonFreshChildWithoutResumeOK(t *testing.T) {
	parent := New() // NO SetBase
	child := New()
	recordFlash(child, deepseek.Usage{PromptTokens: 200, TotalTokens: 200})

	// Must not panic — there's no double-count risk without a parent
	// baseUsage to overlap with.
	parent.AdoptChild(child)

	if got := parent.Cumulative().TotalTokens; got != 200 {
		t.Errorf("Cumulative = %d, want 200 (only the child's turn)", got)
	}
}
