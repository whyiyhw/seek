package bgjob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- ring buffer -----------------------------------------------------

func TestRing_IncrementalRead(t *testing.T) {
	r := newRing(1024)
	if _, err := r.Write([]byte("hello ")); err != nil {
		t.Fatal(err)
	}
	w, cur, gap := r.readFrom(0)
	if string(w) != "hello " || gap != 0 {
		t.Fatalf("first read = %q gap=%d, want %q gap=0", w, gap, "hello ")
	}
	// Nothing new since cur.
	w, cur2, _ := r.readFrom(cur)
	if len(w) != 0 || cur2 != cur {
		t.Fatalf("empty read = %q cur=%d, want empty cur=%d", w, cur2, cur)
	}
	r.Write([]byte("world"))
	w, _, gap = r.readFrom(cur)
	if string(w) != "world" || gap != 0 {
		t.Fatalf("incremental read = %q gap=%d, want %q", w, gap, "world")
	}
}

func TestRing_Overflow(t *testing.T) {
	r := newRing(8)
	r.Write([]byte("0123456789ABCDEF")) // 16 bytes into an 8-byte ring
	w, cur, _ := r.readFrom(0)
	if string(w) != "89ABCDEF" {
		t.Fatalf("retained window = %q, want last 8 %q", w, "89ABCDEF")
	}
	if cur != 16 {
		t.Fatalf("cursor = %d, want 16 (monotonic absolute offset)", cur)
	}
	// A reader that last saw offset 0 must learn 8 bytes were dropped.
	w, _, gap := r.readFrom(0)
	if gap != 8 {
		t.Fatalf("gap = %d, want 8 dropped bytes", gap)
	}
	if string(w) != "89ABCDEF" {
		t.Fatalf("window after drop = %q", w)
	}
}

// --- launch + poll ---------------------------------------------------

func TestManager_LaunchAndPoll(t *testing.T) {
	m := New()
	j, err := m.Launch("echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != "bg-1" {
		t.Fatalf("id = %q, want bg-1", j.ID)
	}
	j.Write([]byte("partial output"))

	pr, err := m.Poll(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Status != StatusRunning {
		t.Fatalf("status = %v, want running", pr.Status)
	}
	if string(pr.Window) != "partial output" {
		t.Fatalf("window = %q", pr.Window)
	}
	// Second poll: cursor advanced, no repeat.
	pr, _ = m.Poll(j.ID)
	if len(pr.Window) != 0 {
		t.Fatalf("second poll repeated output: %q", pr.Window)
	}

	j.Finish(0)
	pr, _ = m.Poll(j.ID)
	if pr.Status != StatusExited || pr.ExitCode != 0 {
		t.Fatalf("after Finish: status=%v code=%d, want exited 0", pr.Status, pr.ExitCode)
	}
}

func TestManager_ConcurrencyCap(t *testing.T) {
	m := New(WithMaxJobs(2))
	if _, err := m.Launch("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Launch("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Launch("c"); err == nil {
		t.Fatal("3rd launch should fail at cap 2")
	}
	// Finishing one frees a slot.
	j, _ := m.Get("bg-1")
	j.Finish(0)
	if _, err := m.Launch("d"); err != nil {
		t.Fatalf("launch after a slot freed should succeed: %v", err)
	}
}

// --- wait ------------------------------------------------------------

func TestManager_Wait_Exit(t *testing.T) {
	m := New()
	j, _ := m.Launch("sleep")
	go func() {
		time.Sleep(20 * time.Millisecond)
		j.Write([]byte("done\n"))
		j.Finish(3)
	}()
	wr, err := m.Wait(context.Background(), j.ID, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Reason != ReasonExited || wr.Status != StatusExited || wr.ExitCode != 3 {
		t.Fatalf("wait = reason %v status %v code %d, want exited/exited/3", wr.Reason, wr.Status, wr.ExitCode)
	}
	if !strings.Contains(string(wr.Window), "done") {
		t.Fatalf("wait window = %q, want it to include final output", wr.Window)
	}
}

func TestManager_Wait_UntilRegex(t *testing.T) {
	m := New()
	j, _ := m.Launch("server")
	// Write the trigger line AFTER Wait begins → exercises the tick path,
	// not just the pre-block fast path.
	go func() {
		time.Sleep(20 * time.Millisecond)
		j.Write([]byte("booting...\nListening on :8080\n"))
	}()
	wr, err := m.Wait(context.Background(), j.ID, `Listening on :\d+`, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Reason != ReasonMatched {
		t.Fatalf("reason = %v, want matched", wr.Reason)
	}
	if wr.Status != StatusRunning {
		t.Fatalf("status = %v, want still running (regex match must not kill)", wr.Status)
	}
}

func TestManager_Wait_Timeout(t *testing.T) {
	m := New()
	j, _ := m.Launch("hang")
	wr, err := m.Wait(context.Background(), j.ID, "", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Reason != ReasonTimeout || wr.Status != StatusRunning {
		t.Fatalf("wait = reason %v status %v, want timeout/running", wr.Reason, wr.Status)
	}
}

// The load-bearing one: cancelling a Wait (Esc) must stop observing but
// leave the job alive.
func TestManager_Wait_CtxCancel_JobSurvives(t *testing.T) {
	m := New()
	j, _ := m.Launch("longrunner")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	wr, err := m.Wait(ctx, j.ID, "", 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if wr.Reason != ReasonCancelled {
		t.Fatalf("reason = %v, want cancelled", wr.Reason)
	}
	if pr, _ := m.Poll(j.ID); pr.Status != StatusRunning {
		t.Fatalf("job status = %v after wait-cancel, want STILL running", pr.Status)
	}
}

func TestManager_Wait_BadRegex(t *testing.T) {
	m := New()
	j, _ := m.Launch("x")
	if _, err := m.Wait(context.Background(), j.ID, "(", time.Second); err == nil {
		t.Fatal("bad regex should error without blocking")
	}
}

// --- kill / shutdown -------------------------------------------------

func TestManager_Kill(t *testing.T) {
	m := New()
	j, _ := m.Launch("victim")
	var killed atomic.Bool
	j.SetKiller(func() error { killed.Store(true); return nil })

	if err := m.Kill(j.ID); err != nil {
		t.Fatal(err)
	}
	if !killed.Load() {
		t.Fatal("killer closure not invoked")
	}
	if pr, _ := m.Poll(j.ID); pr.Status != StatusKilled {
		t.Fatalf("status = %v, want killed", pr.Status)
	}
	// Killing an already-dead job is a no-op, not an error.
	if err := m.Kill(j.ID); err != nil {
		t.Fatalf("re-kill = %v, want nil no-op", err)
	}
}

func TestManager_Shutdown_KillsRunningOnly(t *testing.T) {
	m := New()
	var killCount atomic.Int32
	mk := func() error { killCount.Add(1); return nil }

	r1, _ := m.Launch("run1")
	r1.SetKiller(mk)
	r2, _ := m.Launch("run2")
	r2.SetKiller(mk)
	fin, _ := m.Launch("finished")
	fin.SetKiller(mk)
	fin.Finish(0) // already exited → Shutdown must NOT kill it

	m.Shutdown()

	if got := killCount.Load(); got != 2 {
		t.Fatalf("kills = %d, want 2 (only the running jobs)", got)
	}
	if pr, _ := m.Poll(fin.ID); pr.Status != StatusExited {
		t.Fatalf("finished job status = %v, want still exited", pr.Status)
	}
}

func TestManager_UnknownJob(t *testing.T) {
	m := New()
	if _, err := m.Poll("bg-99"); err == nil {
		t.Fatal("poll unknown should error")
	}
	if _, err := m.Wait(context.Background(), "bg-99", "", time.Second); err == nil {
		t.Fatal("wait unknown should error")
	}
	if err := m.Kill("bg-99"); err == nil {
		t.Fatal("kill unknown should error")
	}
}

// --- concurrency / races --------------------------------------------

// Kill racing the wait goroutine's Finish must transition exactly once
// (single close of done). Loop so -race / the panic-on-double-close has
// many windows to fire.
func TestManager_KillVsFinish_Race(t *testing.T) {
	for i := 0; i < 200; i++ {
		m := New()
		j, _ := m.Launch("race")
		j.SetKiller(func() error { return nil })
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); j.Finish(0) }()
		go func() { defer wg.Done(); _ = m.Kill(j.ID) }()
		wg.Wait()
		pr, _ := m.Poll(j.ID)
		if pr.Status != StatusExited && pr.Status != StatusKilled {
			t.Fatalf("status = %v, want exited or killed", pr.Status)
		}
	}
}

func TestManager_Concurrent(t *testing.T) {
	m := New(WithMaxJobs(64), WithRingCap(256))
	var ids []string
	for i := 0; i < 16; i++ {
		j, err := m.Launch(fmt.Sprintf("job-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		j.SetKiller(func() error { return nil })
		ids = append(ids, j.ID)
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		j, _ := m.Get(id)
		// writer
		wg.Add(1)
		go func(j *Job) {
			defer wg.Done()
			for k := 0; k < 100; k++ {
				j.Write(bytes.Repeat([]byte("x"), 10))
			}
		}(j)
		// poller
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for k := 0; k < 100; k++ {
				_, _ = m.Poll(id)
			}
		}(id)
	}
	// concurrent shutdown
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		m.Shutdown()
	}()
	wg.Wait()
}
