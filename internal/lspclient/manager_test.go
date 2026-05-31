package lspclient

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDetectLang(t *testing.T) {
	cases := []struct {
		file string
		lang string
		ok   bool
	}{
		{"a.go", "go", true},
		{"pkg/b.py", "python", true},
		{"src/c.ts", "typescript", true},
		{"d.tsx", "typescript", true},
		{"e.jsx", "typescript", true},
		{"f.rs", "", false},
		{"Makefile", "", false},
	}
	for _, tc := range cases {
		l, ok := detectLang(tc.file)
		if ok != tc.ok || (ok && l.id != tc.lang) {
			t.Errorf("detectLang(%q) = (%q, %v), want (%q, %v)", tc.file, l.id, ok, tc.lang, tc.ok)
		}
	}
}

func TestDefaultLaunch_MissingBinary(t *testing.T) {
	_, err := defaultLaunch(context.Background(), language{
		command: "definitely-not-a-real-binary-xyz123",
		install: "go install …",
	})
	var mbe *MissingBinaryError
	if !errors.As(err, &mbe) {
		t.Fatalf("err = %v, want *MissingBinaryError", err)
	}
}

// fakeManager builds a Manager whose launch returns mock-backed clients,
// recording how many times launch was called.
func fakeManager(t *testing.T, serve func(*bufio.Reader, *io.PipeWriter)) (*Manager, *int32, *[]*Client) {
	t.Helper()
	var count int32
	var created []*Client
	m := New(t.TempDir(), context.Background())
	m.launch = func(ctx context.Context, lang language) (*Client, error) {
		atomic.AddInt32(&count, 1)
		c := newTestClient(t, serve)
		created = append(created, c)
		return c, nil
	}
	return m, &count, &created
}

func TestManager_LazyStart_OnePerLang(t *testing.T) {
	m, count, _ := fakeManager(t, serveStd([]Location{{URI: "file:///a.go"}}))
	file := filepath.Join(m.rootDir, "x.go")
	pos := Position{Line: 0, Character: 0}

	for i := 0; i < 2; i++ {
		if _, err := m.References(context.Background(), file, "package x", pos); err != nil {
			t.Fatalf("References #%d: %v", i, err)
		}
	}
	if n := atomic.LoadInt32(count); n != 1 {
		t.Fatalf("launch called %d times, want 1 (one server per language, reused)", n)
	}
}

func TestManager_RestartAfterCrash(t *testing.T) {
	m, count, created := fakeManager(t, serveStd([]Location{{URI: "file:///a.go"}}))
	file := filepath.Join(m.rootDir, "x.go")
	pos := Position{Line: 0, Character: 0}

	if _, err := m.References(context.Background(), file, "package x", pos); err != nil {
		t.Fatal(err)
	}
	// Simulate the server dying (crash detection itself is covered by
	// TestClient_ServerCrash; here we just need Alive() to flip false).
	(*created)[0].Close()

	if _, err := m.References(context.Background(), file, "package x", pos); err != nil {
		t.Fatalf("References after crash: %v", err)
	}
	if n := atomic.LoadInt32(count); n != 2 {
		t.Fatalf("launch called %d times, want 2 (dead server must be restarted)", n)
	}
}

func TestManager_MissingBinary(t *testing.T) {
	m := New(t.TempDir(), context.Background())
	m.launch = func(ctx context.Context, lang language) (*Client, error) {
		return nil, &MissingBinaryError{Command: lang.command, Install: lang.install}
	}
	_, err := m.References(context.Background(), filepath.Join(m.rootDir, "x.go"), "package x", Position{})
	var mbe *MissingBinaryError
	if !errors.As(err, &mbe) {
		t.Fatalf("err = %v, want *MissingBinaryError surfaced to the caller", err)
	}
}

func TestManager_UnsupportedExtension(t *testing.T) {
	m, _, _ := fakeManager(t, serveStd(nil))
	_, err := m.References(context.Background(), filepath.Join(m.rootDir, "x.rs"), "", Position{})
	if err == nil {
		t.Fatal("a .rs file has no configured server — should error, not start one")
	}
}

func TestManager_Shutdown_ClosesAll(t *testing.T) {
	m, _, created := fakeManager(t, serveStd([]Location{{URI: "file:///a.go"}}))
	if _, err := m.References(context.Background(), filepath.Join(m.rootDir, "x.go"), "package x", Position{}); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	if (*created)[0].Alive() {
		t.Fatal("Shutdown must close every server")
	}
	m.mu.Lock()
	n := len(m.servers)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("servers map = %d entries after Shutdown, want 0", n)
	}
}

// The D4 crown-jewel test: a turn cancel during cold start must NOT kill
// or restart the server. The first (impatient) caller gives up; the
// server keeps initializing under the session ctx and stays cached; the
// next caller reuses it — launch fires exactly once.
func TestManager_ColdStartCtxCancel_KeepsServer(t *testing.T) {
	slow := func(in *bufio.Reader, out *io.PipeWriter) {
		for {
			msg, ok := mockReadReq(in)
			if !ok {
				return
			}
			switch msg.Method {
			case "initialize":
				time.Sleep(200 * time.Millisecond) // slow cold start
				mockResult(out, msg.ID, map[string]any{"capabilities": map[string]any{}})
			case "shutdown":
				mockResult(out, msg.ID, nil)
			case "textDocument/references":
				mockResult(out, msg.ID, []Location{{URI: "file:///a.go"}})
			}
		}
	}
	m, count, _ := fakeManager(t, slow)
	file := filepath.Join(m.rootDir, "x.go")
	pos := Position{Line: 0, Character: 0}

	// First caller: short ctx, gives up during the 200ms cold start.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel1()
	_, err := m.References(ctx1, file, "package x", pos)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first call err = %v, want DeadlineExceeded", err)
	}

	// Second caller: patient. The still-initializing server is reused.
	if _, err := m.References(context.Background(), file, "package x", pos); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n := atomic.LoadInt32(count); n != 1 {
		t.Fatalf("launch called %d times, want 1 (cold-start cancel must not restart the server — D4)", n)
	}
}
