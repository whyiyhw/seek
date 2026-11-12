package servecli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/controlplane"
	"github.com/whyiyhw/seek/internal/controlplane/fake"
)

// withHome isolates SEEK_HOME so config.Load/Save and (later) the
// bindings store never touch the real ~/.seek.
func withHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_HOME", dir)
	return dir
}

// waitForCancel blocks until fn returns or the deadline, then reports
// fn's values. Serve-plane tests must never hang the suite on a
// lifecycle bug.
func waitForCancel(t *testing.T, d time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatal("servePlane did not return after ctx cancel")
		return nil
	}
}

// cancelAfter builds a context cancelled after d, from this goroutine
// (go vet's lostcancel).
func cancelAfter(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(d, cancel)
	return ctx, cancel
}

// TestServePlane_FakeFrontend_CleanStartStop is the M1b acceptance in
// unit form: fake frontend up, ctx cancelled (the signal stand-in),
// plane drains and Serve returns nil → exit code 0.
func TestServePlane_FakeFrontend_CleanStartStop(t *testing.T) {
	withHome(t)
	ctx, cancel := cancelAfter(t, 100*time.Millisecond)
	defer cancel()

	var stderr bytes.Buffer
	err := waitForCancel(t, 10*time.Second, func() error {
		return servePlane(ctx, []controlplane.Frontend{fake.New()}, config.Config{}, &stderr)
	})
	if err != nil {
		t.Fatalf("empty plane start/stop must be clean, got %v", err)
	}
	out := stderr.String()
	if !strings.Contains(out, "plane up") || !strings.Contains(out, "frontends=[fake]") {
		t.Errorf("startup log missing plane line: %s", out)
	}
	if !strings.Contains(out, "drained cleanly") {
		t.Errorf("clean-shutdown log missing: %s", out)
	}
	if !strings.Contains(out, "yolo=false") {
		t.Errorf("no serve section must log the safe default yolo=false: %s", out)
	}
}

// TestServePlane_OldConfigNoServeSection: a config written before the
// plane existed (no serve key) must not error the plane — the compat
// acceptance, driven through the real config file.
func TestServePlane_OldConfigNoServeSection(t *testing.T) {
	dir := withHome(t)
	old := `{"default_provider": "deepseek", "providers": {"deepseek": {"api_key": "sk-x"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("old config must load: %v", err)
	}

	ctx, cancel := cancelAfter(t, 100*time.Millisecond)
	defer cancel()
	var stderr bytes.Buffer
	err = waitForCancel(t, 10*time.Second, func() error {
		return servePlane(ctx, []controlplane.Frontend{fake.New()}, cfg, &stderr)
	})
	if err != nil {
		t.Fatalf("old config must not break servePlane: %v", err)
	}
}

// TestServePlane_ServeSectionWired: the serve section maps onto the
// startup posture — effective max_parallel from config, yolo reflected.
func TestServePlane_ServeSectionWired(t *testing.T) {
	withHome(t)
	cfg := config.Config{Serve: &config.ServeConfig{
		Projects:       []string{"/a", "/b"},
		MaxConcurrency: 7,
		Yolo:           true,
	}}
	ctx, cancel := cancelAfter(t, 100*time.Millisecond)
	defer cancel()
	var stderr bytes.Buffer
	err := waitForCancel(t, 10*time.Second, func() error {
		return servePlane(ctx, []controlplane.Frontend{fake.New()}, cfg, &stderr)
	})
	if err != nil {
		t.Fatal(err)
	}
	out := stderr.String()
	for _, want := range []string{"max_parallel=7", "yolo=true", "candidate_projects=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("startup log missing %q: %s", want, out)
		}
	}
}

// TestBuildFrontends covers the registry paths: none / unknown / dup /
// resolved. Wrapping into the exit-2 error class happens in Run's
// badConfig — asserted end-to-end by TestRun_UsageErrors_ExitCode2.
func TestBuildFrontends(t *testing.T) {
	if _, err := buildFrontends(nil); err == nil || !strings.Contains(err.Error(), "no frontends") {
		t.Errorf("no frontend must be rejected with a hint, got %v", err)
	}
	_, err := buildFrontends([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "fake") {
		t.Errorf("unknown frontend must name the registered ones, got %v", err)
	}
	if _, err := buildFrontends([]string{"fake", "fake"}); err != nil {
		t.Errorf("duplicates belong to Serve, not buildFrontends: %v", err)
	}
	got, err := buildFrontends([]string{"fake"})
	if err != nil || len(got) != 1 || got[0].Name() != "fake" {
		t.Errorf("fake must resolve: %v %v", got, err)
	}
}

// TestRun_UsageErrors_ExitCode2: unknown frontend and stray args are
// usage-shaped — ExitCode must route them to 2, and the message must
// carry the reason.
func TestRun_UsageErrors_ExitCode2(t *testing.T) {
	withHome(t)
	for _, args := range [][]string{
		{"--frontend=bogus"},
		{"extra-arg"},
		{},
	} {
		var out, errOut bytes.Buffer
		err := Run(args, &out, &errOut)
		if err == nil {
			t.Fatalf("Run(%v) must fail", args)
		}
		if code := controlplane.ExitCode(err); code != controlplane.ExitFrontendsDown {
			t.Errorf("Run(%v): exit code = %d, want 2 (err %v)", args, code, err)
		}
	}
}

// TestRun_Help: bare `seek serve help` prints usage and exits clean
// (usage display is not an error).
func TestRun_Help(t *testing.T) {
	withHome(t)
	var out bytes.Buffer
	if err := Run([]string{"help"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("help must be clean: %v", err)
	}
	if !strings.Contains(out.String(), "--frontend") {
		t.Errorf("help missing --frontend: %s", out.String())
	}
}
