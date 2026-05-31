package goal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeWorker / fakeJudge are func-driven so each test shapes behaviour by
// turn number. fn receives ctx so cancellation/timeout tests can observe
// the (possibly timeout-wrapped) context the driver hands the worker.
type fakeWorker struct {
	fn    func(ctx context.Context, turn int, directive string) (TurnResult, error)
	calls []string
}

func (w *fakeWorker) RunTurn(ctx context.Context, directive string) (TurnResult, error) {
	w.calls = append(w.calls, directive)
	return w.fn(ctx, len(w.calls), directive)
}

type fakeJudge struct {
	fn func(turn int, condition string, last TurnResult) (Verdict, error)
	n  int
}

func (j *fakeJudge) Judge(_ context.Context, condition string, last TurnResult) (Verdict, error) {
	j.n++
	return j.fn(j.n, condition, last)
}

func neverMet() *fakeJudge {
	return &fakeJudge{fn: func(int, string, TurnResult) (Verdict, error) { return Verdict{Met: false}, nil }}
}
func progressWorker() *fakeWorker {
	return &fakeWorker{fn: func(context.Context, int, string) (TurnResult, error) {
		return TurnResult{ToolCalls: 1}, nil
	}}
}

func TestRun_MetFirstTurn(t *testing.T) {
	w := &fakeWorker{fn: func(context.Context, int, string) (TurnResult, error) {
		return TurnResult{Text: "done", ToolCalls: 1, Tokens: 10}, nil
	}}
	j := &fakeJudge{fn: func(int, string, TurnResult) (Verdict, error) {
		return Verdict{Met: true, Reason: "ok"}, nil
	}}
	rep, err := New(w, j, Caps{}).Run(context.Background(), "make X")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Met || rep.Stop != StopMet || rep.Turns != 1 || rep.Tokens != 10 {
		t.Fatalf("rep = %+v", rep)
	}
	if len(w.calls) != 1 || w.calls[0] != "make X" {
		t.Fatalf("turn-1 directive must BE the condition, got %v", w.calls)
	}
}

func TestRun_MetAfterProgress_CarriesHint(t *testing.T) {
	j := &fakeJudge{fn: func(turn int, _ string, _ TurnResult) (Verdict, error) {
		if turn < 3 {
			return Verdict{Met: false, Reason: "not yet", Hint: "do more"}, nil
		}
		return Verdict{Met: true, Reason: "finally"}, nil
	}}
	w := progressWorker()
	rep, _ := New(w, j, Caps{}).Run(context.Background(), "cond")
	if rep.Stop != StopMet || rep.Turns != 3 || len(rep.Trace) != 3 {
		t.Fatalf("rep = %+v", rep)
	}
	// Continuation directive must carry the judge's reason + hint (and not
	// be a rewrite of turn 1 — append-only).
	if w.calls[0] != "cond" {
		t.Fatalf("first directive = %q", w.calls[0])
	}
	if !strings.Contains(w.calls[1], "not yet") || !strings.Contains(w.calls[1], "do more") {
		t.Fatalf("continuation must carry judge reason+hint: %q", w.calls[1])
	}
}

func TestRun_MaxTurns(t *testing.T) {
	rep, _ := New(progressWorker(), neverMet(), Caps{MaxTurns: 5}).Run(context.Background(), "x")
	if rep.Stop != StopMaxTurns || rep.Turns != 5 {
		t.Fatalf("rep = %+v", rep)
	}
}

func TestRun_Stall(t *testing.T) {
	noProgress := &fakeWorker{fn: func(context.Context, int, string) (TurnResult, error) {
		return TurnResult{ToolCalls: 0}, nil
	}}
	rep, _ := New(noProgress, neverMet(), Caps{MaxTurns: 50, StallLimit: 3}).Run(context.Background(), "x")
	if rep.Stop != StopStalled || rep.Turns != 3 {
		t.Fatalf("rep = %+v", rep)
	}
}

func TestRun_StallResetsOnProgress(t *testing.T) {
	// no, no, PROGRESS, no, no, no → stall fires at turn 6 (counter reset by turn 3)
	w := &fakeWorker{fn: func(_ context.Context, turn int, _ string) (TurnResult, error) {
		if turn == 3 {
			return TurnResult{ToolCalls: 2}, nil
		}
		return TurnResult{ToolCalls: 0}, nil
	}}
	rep, _ := New(w, neverMet(), Caps{MaxTurns: 50, StallLimit: 3}).Run(context.Background(), "x")
	if rep.Stop != StopStalled || rep.Turns != 6 {
		t.Fatalf("stall counter must reset on a progress turn: %+v", rep)
	}
}

func TestRun_TokenBudget(t *testing.T) {
	w := &fakeWorker{fn: func(context.Context, int, string) (TurnResult, error) {
		return TurnResult{ToolCalls: 1, Tokens: 100}, nil
	}}
	rep, _ := New(w, neverMet(), Caps{MaxTurns: 50, TokenBudget: 250}).Run(context.Background(), "x")
	if rep.Stop != StopBudget || rep.Turns != 3 || rep.Tokens != 300 {
		t.Fatalf("budget stops after the turn that crosses it: %+v", rep)
	}
}

func TestRun_JudgeErrorContinues(t *testing.T) {
	j := &fakeJudge{fn: func(int, string, TurnResult) (Verdict, error) {
		return Verdict{}, errors.New("boom")
	}}
	rep, _ := New(progressWorker(), j, Caps{MaxTurns: 4}).Run(context.Background(), "x")
	if rep.Stop != StopMaxTurns {
		t.Fatalf("a flaky judge must NOT abort the loop: %+v", rep)
	}
	if len(rep.Trace) == 0 || !strings.Contains(rep.Trace[0].Reason, "judge error") {
		t.Fatalf("judge error should be recorded as not-met: %+v", rep.Trace)
	}
}

func TestRun_WorkerError(t *testing.T) {
	w := &fakeWorker{fn: func(context.Context, int, string) (TurnResult, error) {
		return TurnResult{}, errors.New("worker boom")
	}}
	rep, _ := New(w, neverMet(), Caps{}).Run(context.Background(), "x")
	if rep.Stop != StopError || rep.Turns != 1 || !strings.Contains(rep.Reason, "worker boom") {
		t.Fatalf("rep = %+v", rep)
	}
}

func TestRun_PreCanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &fakeWorker{fn: func(context.Context, int, string) (TurnResult, error) {
		t.Fatal("worker must not run on an already-canceled ctx")
		return TurnResult{}, nil
	}}
	rep, _ := New(w, neverMet(), Caps{}).Run(ctx, "x")
	if rep.Stop != StopCanceled || rep.Turns != 0 {
		t.Fatalf("rep = %+v", rep)
	}
}

func TestRun_CanceledMidLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &fakeWorker{fn: func(c context.Context, _ int, _ string) (TurnResult, error) {
		cancel() // user hit Esc mid-turn
		return TurnResult{}, c.Err()
	}}
	rep, _ := New(w, neverMet(), Caps{}).Run(ctx, "x")
	if rep.Stop != StopCanceled {
		t.Fatalf("rep = %+v", rep)
	}
}

func TestRun_Timeout(t *testing.T) {
	// Worker blocks on the (timeout-wrapped) ctx the driver hands it; the
	// Caps.Timeout fires → DeadlineExceeded → StopTimeout.
	w := &fakeWorker{fn: func(c context.Context, _ int, _ string) (TurnResult, error) {
		<-c.Done()
		return TurnResult{}, c.Err()
	}}
	rep, _ := New(w, neverMet(), Caps{Timeout: 20 * time.Millisecond}).Run(context.Background(), "x")
	if rep.Stop != StopTimeout {
		t.Fatalf("rep = %+v", rep)
	}
}

func TestCaps_Defaults(t *testing.T) {
	c := Caps{}.WithDefaults()
	if c.MaxTurns != defaultMaxTurns || c.StallLimit != defaultStallLimit {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestRun_OnTurnFires(t *testing.T) {
	var logged []TurnLog
	d := New(progressWorker(), neverMet(), Caps{MaxTurns: 3})
	d.OnTurn = func(tl TurnLog) { logged = append(logged, tl) }
	d.Run(context.Background(), "x")
	if len(logged) != 3 || logged[0].Turn != 1 || logged[2].Turn != 3 {
		t.Fatalf("OnTurn should fire once per turn with the turn log: %+v", logged)
	}
}
