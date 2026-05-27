package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestWillUseTUI(t *testing.T) {
	if !willUseTUI(false, "", "", false, false) {
		t.Fatal("plain seek launch should use TUI")
	}
	if willUseTUI(true, "", "", false, false) {
		t.Fatal("-json should not use TUI")
	}
	if willUseTUI(false, "hi", "", false, false) {
		t.Fatal("-p should not use TUI")
	}
	if willUseTUI(false, "", "self-hosting", false, false) {
		t.Fatal("-benchmark should not use TUI")
	}
	if willUseTUI(false, "", "", true, false) {
		t.Fatal("-rpc should not use TUI")
	}
	if willUseTUI(false, "", "", false, true) {
		t.Fatal("-dream should not use TUI")
	}
}

func TestRunInstall_nonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows PATH install needs integration coverage")
	}
	err := runInstall()
	if err == nil {
		t.Fatal("expected error on non-Windows")
	}
	if !strings.Contains(err.Error(), "only supported on Windows") {
		t.Fatalf("unexpected error: %v", err)
	}
}
