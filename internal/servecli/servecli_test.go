package servecli

import (
	"bytes"
	"context"
	"net"
	"net/http"
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
	if _, err := buildFrontends(nil, nil); err == nil || !strings.Contains(err.Error(), "no frontends") {
		t.Errorf("no frontend must be rejected with a hint, got %v", err)
	}
	_, err := buildFrontends([]string{"bogus"}, nil)
	if err == nil || !strings.Contains(err.Error(), "fake") {
		t.Errorf("unknown frontend must name the registered ones, got %v", err)
	}
	if _, err := buildFrontends([]string{"fake", "fake"}, nil); err != nil {
		t.Errorf("duplicates belong to Serve, not buildFrontends: %v", err)
	}
	got, err := buildFrontends([]string{"fake"}, nil)
	if err != nil || len(got) != 1 || got[0].Name() != "fake" {
		t.Errorf("fake must resolve: %v %v", got, err)
	}
}

// freeWebAddr grabs an OS-assigned loopback address for a wiring test
// (same accepted listen-close-reuse posture as controlplane's
// freeAddr).
func freeWebAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// TestServePlane_WebAddrFromConfig: serve.web_addr flows from the
// config through buildFrontends into the web frontend's listener —
// the c_x7ehom wiring. The default (section absent) staying loopback
// is NewWeb's own test. The startup banner goes to the frontend's
// stderr default, so the wiring proof here is the listener itself:
// the embedded page must answer on exactly the configured address.
func TestServePlane_WebAddrFromConfig(t *testing.T) {
	withHome(t)
	addr := freeWebAddr(t)
	cfg := config.Config{Serve: &config.ServeConfig{WebAddr: addr}}
	fes, err := buildFrontends([]string{"web"}, cfg.Serve)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- servePlane(ctx, fes, cfg, &stderr) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("web frontend never came up on the configured web_addr")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("plane must drain clean after cancel, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("servePlane did not return after ctx cancel")
	}
}

// TestServePlane_BadWebAddrFails: a serve.web_addr that must not fly —
// malformed (the listen is the syntax validation) or out of scope
// (0.0.0.0 / a LAN IP; the allowlist itself is controlplane's
// TestWeb_AddrScope, 2026-11-15 decision) — fails the plane loudly
// with the exit-2 class, never a silent fallback to the default
// address. (The error naming the address is asserted at the
// controlplane layer, TestWeb_RunBadAddr/TestWeb_RunForbiddenAddr;
// the supervisor's frontend-down line goes to os.Stderr, not this
// buffer.)
func TestServePlane_BadWebAddrFails(t *testing.T) {
	for _, bad := range []string{
		"127.0.0.1:no-port-digits", // syntax: dies at the listen
		"0.0.0.0:8790",             // scope: all interfaces, refused up front
		"192.168.1.5:8790",         // scope: LAN IP, refused up front
	} {
		t.Run(bad, func(t *testing.T) {
			withHome(t)
			cfg := config.Config{Serve: &config.ServeConfig{WebAddr: bad}}
			fes, err := buildFrontends([]string{"web"}, cfg.Serve)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stderr bytes.Buffer
			err = waitForCancel(t, 10*time.Second, func() error {
				return servePlane(ctx, fes, cfg, &stderr)
			})
			if err == nil {
				t.Fatal("bad web_addr must fail the plane")
			}
			if code := controlplane.ExitCode(err); code != controlplane.ExitFrontendsDown {
				t.Errorf("bad web_addr %q exit code = %d, want 2 (err %v)", bad, code, err)
			}
		})
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
