package bash

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/bgjob"
	"github.com/whyiyhw/seek/internal/permission"
)

func yoloBg(t *testing.T) (Tool, *bgjob.Manager) {
	t.Helper()
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	mgr := bgjob.New()
	return New(p).WithBackground(mgr), mgr
}

// runCtx is like run() but with a caller-supplied ctx so we can prove the
// background path ignores it (PRD §4 D5).
func runCtx(t *testing.T, ctx context.Context, tool Tool, a Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return tool.Execute(ctx, b)
}

func TestBash_Background_NonBlocking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	tool, mgr := yoloBg(t)
	defer mgr.Shutdown()

	start := time.Now()
	out, err := run(t, tool, Args{Command: "sleep 10", RunInBackground: true})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("background launch blocked for %s — should return immediately", elapsed)
	}
	if !strings.Contains(out, "[bg: started bg-1]") {
		t.Fatalf("result missing handle: %q", out)
	}
	if !strings.Contains(out, "monitor") {
		t.Fatalf("result should point at the monitor tool: %q", out)
	}
	// The job must be running, not finished.
	if pr, _ := mgr.Poll("bg-1"); pr.Status != bgjob.StatusRunning {
		t.Fatalf("status = %v, want running", pr.Status)
	}
}

func TestBash_Background_CapturesOutputAndExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	tool, mgr := yoloBg(t)
	defer mgr.Shutdown()

	if _, err := run(t, tool, Args{Command: "printf 'hello-bg'; exit 7", RunInBackground: true}); err != nil {
		t.Fatal(err)
	}
	wr, err := mgr.Wait(context.Background(), "bg-1", "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Reason != bgjob.ReasonExited || wr.ExitCode != 7 {
		t.Fatalf("wait = reason %v code %d, want exited/7", wr.Reason, wr.ExitCode)
	}
	if !strings.Contains(string(wr.Window), "hello-bg") {
		t.Fatalf("captured output = %q, want hello-bg", wr.Window)
	}
}

// The load-bearing D5 test at the bash layer: a background job must NOT be
// bound to the turn ctx. Launch it with an ALREADY-cancelled ctx; it must
// still run to completion.
func TestBash_Background_IgnoresTurnCtx(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	tool, mgr := yoloBg(t)
	defer mgr.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // turn already cancelled before the call

	if _, err := runCtx(t, ctx, tool, Args{Command: "sleep 0.2; printf survived", RunInBackground: true}); err != nil {
		t.Fatal(err)
	}
	wr, err := mgr.Wait(context.Background(), "bg-1", "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Reason != bgjob.ReasonExited || wr.ExitCode != 0 {
		t.Fatalf("job = reason %v code %d, want exited/0 despite cancelled turn ctx", wr.Reason, wr.ExitCode)
	}
	if !strings.Contains(string(wr.Window), "survived") {
		t.Fatalf("output = %q, want survived (cancelled turn ctx must not kill bg job)", wr.Window)
	}
}

// Killing a background job terminates the process group. Process-kill
// correctness (group + escaped grandchildren) is covered by the
// foreground TestBash_Timeout_* tests, which exercise the same
// killProcessGroup; here we assert the bash↔manager wiring marks it
// killed and the wait goroutine winds down without leaking.
func TestBash_Background_Kill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	tool, mgr := yoloBg(t)
	defer mgr.Shutdown()

	if _, err := run(t, tool, Args{Command: "sleep 30", RunInBackground: true}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Kill("bg-1"); err != nil {
		t.Fatal(err)
	}
	if pr, _ := mgr.Poll("bg-1"); pr.Status != bgjob.StatusKilled {
		t.Fatalf("status = %v, want killed", pr.Status)
	}
	// A wait on a killed job returns promptly (done was closed by Kill).
	wr, err := mgr.Wait(context.Background(), "bg-1", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if wr.Status != bgjob.StatusKilled {
		t.Fatalf("wait status = %v, want killed", wr.Status)
	}
}

// Without WithBackground wired, run_in_background degrades to a clear
// error and foreground bash keeps working (v6 §2.2 independent rollback).
func TestBash_Background_NoManager(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	tool := New(p) // no WithBackground
	_, err := run(t, tool, Args{Command: "echo hi", RunInBackground: true})
	if err == nil {
		t.Fatal("run_in_background without a manager should error")
	}
	if !strings.Contains(err.Error(), "background execution") {
		t.Fatalf("err = %v, want a clear 'background execution unavailable' message", err)
	}
}
