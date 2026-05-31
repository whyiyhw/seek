package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/bgjob"
	"github.com/whyiyhw/seek/internal/tools"
)

func exec(t *testing.T, ctx context.Context, tool Tool, a Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return tool.Execute(ctx, b)
}

func TestMonitor_Poll(t *testing.T) {
	mgr := bgjob.New()
	job, _ := mgr.Launch("echo")
	job.Write([]byte("line one\n"))
	tool := New(mgr)

	// Default action is poll.
	out, err := exec(t, context.Background(), tool, Args{Job: "bg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[bg-1: running") {
		t.Fatalf("missing status header: %q", out)
	}
	if !strings.Contains(out, "line one") {
		t.Fatalf("missing output: %q", out)
	}
	// Second poll: nothing new.
	out, _ = exec(t, context.Background(), tool, Args{Job: "bg-1", Action: "poll"})
	if !strings.Contains(out, "(no new output)") {
		t.Fatalf("second poll should report no new output: %q", out)
	}
}

func TestMonitor_Poll_Exited(t *testing.T) {
	mgr := bgjob.New()
	job, _ := mgr.Launch("x")
	job.Write([]byte("bye"))
	job.Finish(2)
	out, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[bg-1: exited code=2") {
		t.Fatalf("expected exit status with code, got %q", out)
	}
}

func TestMonitor_Wait_Exit(t *testing.T) {
	mgr := bgjob.New()
	job, _ := mgr.Launch("x")
	go func() {
		time.Sleep(20 * time.Millisecond)
		job.Write([]byte("finished\n"))
		job.Finish(0)
	}()
	out, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1", Action: "wait", TimeoutMS: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[bg-1: exited code=0") || !strings.Contains(out, "finished") {
		t.Fatalf("wait result = %q", out)
	}
}

func TestMonitor_Wait_UntilRegex(t *testing.T) {
	mgr := bgjob.New()
	job, _ := mgr.Launch("server")
	go func() {
		time.Sleep(20 * time.Millisecond)
		job.Write([]byte("Listening on :8080\n"))
	}()
	out, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1", Action: "wait", UntilRegex: "Listening on", TimeoutMS: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "until_regex matched") {
		t.Fatalf("expected match note: %q", out)
	}
	if !strings.Contains(out, "running") {
		t.Fatalf("a regex match must leave the job running: %q", out)
	}
}

func TestMonitor_Wait_Timeout(t *testing.T) {
	mgr := bgjob.New()
	mgr.Launch("hang")
	out, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1", Action: "wait", TimeoutMS: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "wait timed out") || !strings.Contains(out, "running") {
		t.Fatalf("expected timeout while running: %q", out)
	}
}

// Esc during a wait: Execute must propagate ctx.Err() so the agent treats
// the turn as interrupted, and the job must keep running.
func TestMonitor_Wait_CtxCancel(t *testing.T) {
	mgr := bgjob.New()
	mgr.Launch("longrunner")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	_, err := exec(t, ctx, New(mgr), Args{Job: "bg-1", Action: "wait", TimeoutMS: 5000})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled propagated", err)
	}
	if pr, _ := mgr.Poll("bg-1"); pr.Status != bgjob.StatusRunning {
		t.Fatalf("job status = %v after wait-cancel, want still running", pr.Status)
	}
}

func TestMonitor_Wait_BadRegex(t *testing.T) {
	mgr := bgjob.New()
	mgr.Launch("x")
	if _, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1", Action: "wait", UntilRegex: "("}); err == nil {
		t.Fatal("bad until_regex should error")
	}
}

func TestMonitor_Kill(t *testing.T) {
	mgr := bgjob.New()
	job, _ := mgr.Launch("victim")
	var killed bool
	job.SetKiller(func() error { killed = true; return nil })

	out, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1", Action: "kill"})
	if err != nil {
		t.Fatal(err)
	}
	if !killed {
		t.Fatal("killer not invoked")
	}
	if !strings.Contains(out, "[bg-1: killed") {
		t.Fatalf("kill result = %q", out)
	}
}

func TestMonitor_Kill_AlreadyExited(t *testing.T) {
	mgr := bgjob.New()
	job, _ := mgr.Launch("x")
	job.Finish(0)
	// Killing a finished job is a no-op; report its true state.
	out, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1", Action: "kill"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[bg-1: exited code=0") {
		t.Fatalf("kill of finished job should report exited, got %q", out)
	}
}

func TestMonitor_UnknownJob(t *testing.T) {
	mgr := bgjob.New()
	tool := New(mgr)
	for _, action := range []string{"poll", "wait", "kill"} {
		if _, err := exec(t, context.Background(), tool, Args{Job: "bg-99", Action: action}); err == nil {
			t.Fatalf("action %q on unknown job should error", action)
		}
	}
}

func TestMonitor_BadAction(t *testing.T) {
	mgr := bgjob.New()
	mgr.Launch("x")
	if _, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1", Action: "frobnicate"}); err == nil {
		t.Fatal("unknown action should error")
	}
}

func TestMonitor_DroppedOutput(t *testing.T) {
	mgr := bgjob.New(bgjob.WithRingCap(8))
	job, _ := mgr.Launch("noisy")
	job.Write([]byte("0123456789ABCDEF")) // 16 bytes into 8-byte ring
	out, err := exec(t, context.Background(), New(mgr), Args{Job: "bg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "earlier bytes dropped") {
		t.Fatalf("overflow must be flagged: %q", out)
	}
	if !strings.Contains(out, "89ABCDEF") {
		t.Fatalf("retained tail missing: %q", out)
	}
}

func TestMonitor_MissingJob(t *testing.T) {
	mgr := bgjob.New()
	if _, err := exec(t, context.Background(), New(mgr), Args{Action: "poll"}); err == nil {
		t.Fatal("missing job field should error")
	}
}

func TestMonitor_NilManagerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) should panic — registered monitor with no manager is a wiring bug")
		}
	}()
	New(nil)
}

// monitor must NOT be a ReadOnlyTool: kill mutates, and a blocking wait
// must not be batched concurrently with read-only tools. Lock the
// contract so nobody adds ReadOnly() later without thinking it through.
func TestMonitor_NotReadOnly(t *testing.T) {
	var tool tools.Tool = New(bgjob.New())
	if _, ok := tool.(tools.ReadOnlyTool); ok {
		t.Fatal("monitor must not implement ReadOnlyTool (kill mutates; wait blocks)")
	}
}
