package autopilot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDecomposer returns a fixed task list (or error).
type fakeDecomposer struct {
	tasks []Task
	err   error
}

func (f fakeDecomposer) Decompose(ctx context.Context, goal string, max int) ([]Task, error) {
	return f.tasks, f.err
}

// fakeFleet records concurrency and runs a per-task function.
type fakeFleet struct {
	run      func(ctx context.Context, t Task) Outcome
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (f *fakeFleet) Run(ctx context.Context, t Task) Outcome {
	n := f.inFlight.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer f.inFlight.Add(-1)
	return f.run(ctx, t)
}

func tasks(n int) []Task {
	out := make([]Task, n)
	for i := range out {
		out[i] = Task{ID: fmt.Sprintf("t%d", i), Title: fmt.Sprintf("task %d", i), Prompt: "do it"}
	}
	return out
}

func TestDriver_FanOut_AllDone(t *testing.T) {
	fleet := &fakeFleet{run: func(ctx context.Context, tk Task) Outcome {
		return Outcome{Task: tk, Status: "done", Summary: "ok", Worktree: "/wt/" + tk.ID}
	}}
	d := New(fakeDecomposer{tasks: tasks(4)}, fleet, Caps{})
	rep, err := d.Run(context.Background(), "build stuff")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Done != 4 || rep.Failed != 0 || len(rep.Outcomes) != 4 {
		t.Fatalf("report = %+v, want 4 done", rep)
	}
}

func TestDriver_PartialFailure(t *testing.T) {
	fleet := &fakeFleet{run: func(ctx context.Context, tk Task) Outcome {
		if tk.ID == "t1" {
			return Outcome{Task: tk, Status: "failed", Summary: "boom"}
		}
		return Outcome{Task: tk, Status: "done", Summary: "ok"}
	}}
	d := New(fakeDecomposer{tasks: tasks(3)}, fleet, Caps{})
	rep, err := d.Run(context.Background(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Done != 2 || rep.Failed != 1 {
		t.Fatalf("report = done %d failed %d, want 2/1 (one failure must not abort the fleet)", rep.Done, rep.Failed)
	}
}

func TestDriver_MaxTasksCap(t *testing.T) {
	// Decomposer over-produces; the driver must clamp to MaxTasks.
	fleet := &fakeFleet{run: func(ctx context.Context, tk Task) Outcome {
		return Outcome{Task: tk, Status: "done"}
	}}
	d := New(fakeDecomposer{tasks: tasks(20)}, fleet, Caps{MaxTasks: 5})
	rep, _ := d.Run(context.Background(), "g")
	if len(rep.Outcomes) != 5 {
		t.Fatalf("ran %d tasks, want clamped to MaxTasks=5", len(rep.Outcomes))
	}
}

func TestDriver_ConcurrencyCap(t *testing.T) {
	release := make(chan struct{})
	fleet := &fakeFleet{run: func(ctx context.Context, tk Task) Outcome {
		<-release // hold until released so concurrency peaks
		return Outcome{Task: tk, Status: "done"}
	}}
	d := New(fakeDecomposer{tasks: tasks(10)}, fleet, Caps{MaxTasks: 10, MaxConcurrent: 3})

	done := make(chan Report, 1)
	go func() { r, _ := d.Run(context.Background(), "g"); done <- r }()

	// Give goroutines time to saturate the semaphore, then release.
	time.Sleep(50 * time.Millisecond)
	peak := fleet.peak.Load()
	close(release)
	<-done

	if peak > 3 {
		t.Fatalf("peak concurrency = %d, want <= MaxConcurrent=3", peak)
	}
	if peak == 0 {
		t.Fatal("nothing ran")
	}
}

func TestDriver_DecomposeError(t *testing.T) {
	d := New(fakeDecomposer{err: errors.New("model down")}, &fakeFleet{}, Caps{})
	_, err := d.Run(context.Background(), "g")
	if err == nil {
		t.Fatal("decompose error should propagate")
	}
}

func TestDriver_EmptyTasks(t *testing.T) {
	d := New(fakeDecomposer{tasks: nil}, &fakeFleet{}, Caps{})
	rep, err := d.Run(context.Background(), "g")
	if err != nil || len(rep.Outcomes) != 0 {
		t.Fatalf("empty decompose → empty report, got %+v err=%v", rep, err)
	}
}

// Kill-switch: cancelling ctx propagates to in-flight Fleet.Run, which
// returns a failed/canceled outcome; Run returns promptly.
func TestDriver_CtxCancel(t *testing.T) {
	fleet := &fakeFleet{run: func(ctx context.Context, tk Task) Outcome {
		<-ctx.Done() // block until cancelled
		return Outcome{Task: tk, Status: "failed", Summary: "canceled"}
	}}
	d := New(fakeDecomposer{tasks: tasks(4)}, fleet, Caps{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	rep, err := d.Run(ctx, "g")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Failed != 4 {
		t.Fatalf("all tasks should report canceled on kill, got done %d failed %d", rep.Done, rep.Failed)
	}
}

func TestDriver_PanicIsolated(t *testing.T) {
	fleet := &fakeFleet{run: func(ctx context.Context, tk Task) Outcome {
		if tk.ID == "t0" {
			panic("kaboom")
		}
		return Outcome{Task: tk, Status: "done"}
	}}
	d := New(fakeDecomposer{tasks: tasks(3)}, fleet, Caps{})
	rep, err := d.Run(context.Background(), "g")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Done != 2 || rep.Failed != 1 {
		t.Fatalf("a panicking task must be isolated as one failure, got done %d failed %d", rep.Done, rep.Failed)
	}
}

func TestDriver_Timeout(t *testing.T) {
	var ran sync.WaitGroup
	ran.Add(1)
	var once sync.Once
	fleet := &fakeFleet{run: func(ctx context.Context, tk Task) Outcome {
		once.Do(ran.Done)
		<-ctx.Done()
		return Outcome{Task: tk, Status: "failed", Summary: "timed out"}
	}}
	d := New(fakeDecomposer{tasks: tasks(2)}, fleet, Caps{Timeout: 30 * time.Millisecond})
	start := time.Now()
	rep, _ := d.Run(context.Background(), "g")
	if time.Since(start) > 2*time.Second {
		t.Fatal("Timeout cap should bound the run")
	}
	if rep.Failed != 2 {
		t.Fatalf("timed-out tasks should be failed, got %+v", rep)
	}
}
