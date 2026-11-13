package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools/read"
)

// anchorBackend mirrors cmd/seek's acpBackend wiring for the session
// cwd contract: NewSession anchors process+policy to p.Cwd and returns
// the project name (directory base) as the sessionId.
type anchorBackend struct {
	policy *permission.Policy
	anchor Anchor
}

func (b *anchorBackend) Initialize(p InitializeParams) InitializeResult {
	return InitializeResult{ProtocolVersion: p.ProtocolVersion}
}

func (b *anchorBackend) NewSession(p NewSessionParams) (NewSessionResult, error) {
	a, err := AnchorSession(p.Cwd, b.policy)
	if err != nil {
		return NewSessionResult{}, err
	}
	b.anchor = a
	return NewSessionResult{SessionID: a.Name}, nil
}

func (b *anchorBackend) Prompt(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error) {
	return PromptResult{StopReason: "end_turn"}, nil
}

// keepCwd restores the process working directory on exit —
// AnchorSession chdirs the WHOLE process (the git/grep convention), so
// a leaking chdir would poison every later test in the package.
func keepCwd(t *testing.T) string {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return old
}

// TestAnchorSession_NewSessionCwdEffective is the D-1 acceptance test
// (feature-paseo-provider.md §2.2): after initialize + session/new
// with cwd=<tmpdir>, a read tool constructed on the session policy —
// exactly how cmd/seek's registry wires it — resolves a RELATIVE path
// into that directory. Before the handshake the same read must miss
// (the file only exists in the session dir), proving the flip.
func TestAnchorSession_NewSessionCwdEffective(t *testing.T) {
	oldCwd := keepCwd(t)
	// Resolve symlinks so os.Getwd() after chdir equals the expected
	// string verbatim (on macOS t.TempDir lives under /var → /private/var).
	sessionDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const fname, body = "d1-probe.txt", "session-cwd-probe"
	if err := os.WriteFile(filepath.Join(sessionDir, fname), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Policy anchored at the OLD cwd, as seek startup does — tools
	// capture this pointer at construction and must follow the
	// session/new re-anchor without being rebuilt.
	pol, err := permission.New(oldCwd, permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	rd := read.New(pol)

	// Sanity: relative read misses BEFORE the handshake (file is only
	// in the session dir, policy still anchored at oldCwd).
	if _, err := rd.Execute(context.Background(), json.RawMessage(`{"path":"`+fname+`"}`)); err == nil {
		t.Fatal("relative read should miss before session/new anchors the cwd")
	}

	b := &anchorBackend{policy: pol}
	enc, dec := startServer(t, b)

	send(t, enc, 1, "initialize", InitializeParams{ProtocolVersion: 1})
	var initRes InitializeResult
	if err := json.Unmarshal(readMsg(t, dec)["result"], &initRes); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if initRes.ProtocolVersion != 1 {
		t.Fatalf("initialize echo = %d", initRes.ProtocolVersion)
	}

	send(t, enc, 2, "session/new", NewSessionParams{Cwd: sessionDir})
	msg := readMsg(t, dec)
	if errRaw, hasErr := msg["error"]; hasErr {
		t.Fatalf("session/new errored: %s", errRaw)
	}
	var res NewSessionResult
	if err := json.Unmarshal(msg["result"], &res); err != nil {
		t.Fatal(err)
	}

	// Project name = directory base, returned as the sessionId.
	if want := filepath.Base(sessionDir); res.SessionID != want {
		t.Fatalf("sessionId = %q, want project name %q", res.SessionID, want)
	}
	// ProjectID = paths.ProjectID(abs(cwd)).
	abs := sessionDir // already absolute + symlink-resolved
	if b.anchor.ProjectID != paths.ProjectID(abs) {
		t.Fatalf("ProjectID = %q, want %q", b.anchor.ProjectID, paths.ProjectID(abs))
	}
	// Policy anchor moved (read/edit/write/bash convention)…
	if got := pol.Cwd(); got != abs {
		t.Fatalf("policy cwd = %q, want %q", got, abs)
	}
	// …and the process anchor moved too (git/grep/list_dir convention).
	if got, _ := os.Getwd(); got != abs {
		t.Fatalf("process cwd = %q, want %q", got, abs)
	}

	// The card's core assertion: relative read lands in the session dir.
	out, err := rd.Execute(context.Background(), json.RawMessage(`{"path":"`+fname+`"}`))
	if err != nil {
		t.Fatalf("relative read after session/new: %v", err)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("read resolved outside the session dir, got %q", out)
	}
}

// TestAnchorSession_DefaultCwdKeepsCurrentDirectory pins the "缺省仍为
// 当前目录" branch: an empty cwd parameter anchors to the process cwd
// (Zed-equivalent behaviour — no cwd, no change).
func TestAnchorSession_DefaultCwdKeepsCurrentDirectory(t *testing.T) {
	oldCwd := keepCwd(t)

	pol, err := permission.New(oldCwd, permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	b := &anchorBackend{policy: pol}
	enc, dec := startServer(t, b)

	send(t, enc, 1, "session/new", NewSessionParams{})
	msg := readMsg(t, dec)
	if errRaw, hasErr := msg["error"]; hasErr {
		t.Fatalf("session/new without cwd errored: %s", errRaw)
	}
	var res NewSessionResult
	if err := json.Unmarshal(msg["result"], &res); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(oldCwd); res.SessionID != want {
		t.Fatalf("sessionId = %q, want current dir base %q", res.SessionID, want)
	}
	if got := pol.Cwd(); got != oldCwd {
		t.Fatalf("policy cwd = %q, want unchanged %q", got, oldCwd)
	}
}

// TestAnchorSession_BadCwdFailsLoud pins fail-loud over silent-wrong-dir
// (D-1 minimal option): a cwd that can't be chdir'd into rejects the
// session with a protocol error instead of running in the daemon dir.
func TestAnchorSession_BadCwdFailsLoud(t *testing.T) {
	keepCwd(t)
	pol, err := permission.New(t.TempDir(), permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	b := &anchorBackend{policy: pol}
	enc, dec := startServer(t, b)

	send(t, enc, 1, "session/new", NewSessionParams{Cwd: filepath.Join(t.TempDir(), "no-such-dir")})
	msg := readMsg(t, dec)
	errRaw, hasErr := msg["error"]
	if !hasErr {
		t.Fatalf("session/new with bad cwd should fail loud, got %v", msg)
	}
	var e struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(errRaw, &e); err != nil {
		t.Fatal(err)
	}
	if e.Code != -32000 || !strings.Contains(e.Message, "no-such-dir") {
		t.Fatalf("error = %d %q, want -32000 naming the bad cwd", e.Code, e.Message)
	}
}
