package bash

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

func yolo(t *testing.T) Tool {
	t.Helper()
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	return New(p)
}

func run(t *testing.T, tool Tool, a Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return tool.Execute(context.Background(), b)
}

func TestBash_DeniedWithoutYolo(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.PrefDeny)
	_, err := run(t, New(p), Args{Command: "echo hi"})
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestBash_EchoUnderYolo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "echo hello-seek"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello-seek") {
		t.Errorf("missing output: %s", out)
	}
	if !strings.Contains(out, "exit=0") {
		t.Errorf("missing exit code: %s", out)
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "false"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "exit=1") {
		t.Errorf("expected exit=1: %s", out)
	}
}

func TestBash_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "sleep 5", TimeoutMS: 200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "TIMED OUT") {
		t.Errorf("expected timeout marker: %s", out)
	}
}

func TestBash_MissingCommand(t *testing.T) {
	_, err := run(t, yolo(t), Args{Command: ""})
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Errorf("err = %v", err)
	}
}

func TestBash_TruncatesHugeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	// 64 KiB of output; should be truncated to 32 KiB.
	out, err := run(t, yolo(t), Args{Command: "yes a | head -c 65536"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "output truncated") {
		t.Errorf("expected truncation: %s", out[:200])
	}
}
