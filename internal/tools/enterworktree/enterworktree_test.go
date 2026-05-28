package enterworktree

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/worktree"
)

// fakeGit is a scriptable git runner mirroring the one in
// worktree_test.go; duplicated here because gitRunner-style
// scripted-response stubs are package-local by convention. Tool
// tests only need a few scripted responses each.
type fakeGit struct {
	mu    sync.Mutex
	queue []resp
	calls [][]string
}
type resp struct {
	stdout, stderr string
	err            error
}

func (f *fakeGit) push(o, e string, err error) *fakeGit {
	f.queue = append(f.queue, resp{o, e, err})
	return f
}

func (f *fakeGit) fn(t *testing.T) worktree.GitRunner {
	t.Helper()
	return func(ctx context.Context, cwd string, args ...string) (string, string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, args)
		if len(f.queue) == 0 {
			t.Fatalf("fakeGit: no queued response for: git %v", args)
		}
		r := f.queue[0]
		f.queue = f.queue[1:]
		return r.stdout, r.stderr, r.err
	}
}

func newTool(t *testing.T, fg *fakeGit) *Tool {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	mgr, err := worktree.NewManagerWithRunner(t.TempDir(), fg.fn(t))
	if err != nil {
		t.Fatal(err)
	}
	return New(mgr)
}

func TestTool_Name(t *testing.T) {
	tl := newTool(t, &fakeGit{})
	if tl.Name() != "enter_worktree" {
		t.Errorf("Name = %q", tl.Name())
	}
}

// TestTool_SchemaIdempotent confirms Schema() returns the same
// bytes across calls — load-bearing for prefix cache (the schema
// goes into the tool list verbatim every turn).
func TestTool_SchemaIdempotent(t *testing.T) {
	tl := newTool(t, &fakeGit{})
	a, b := tl.Schema(), tl.Schema()
	if string(a) != string(b) {
		t.Error("Schema() not idempotent")
	}
	// Sanity: parses as JSON.
	var probe map[string]any
	if err := json.Unmarshal(a, &probe); err != nil {
		t.Errorf("schema bytes not valid JSON: %v", err)
	}
}

// TestExecute_HappyPath: returns the [worktree: created ...] wire
// format with path / branch / base. Verifies the prefix is
// byte-stable.
func TestExecute_HappyPath(t *testing.T) {
	fg := (&fakeGit{}).
		push("abc123\n", "", nil). // rev-parse
		push("", "", nil).          // worktree add
		push("", "", nil)           // update-ref
	tl := newTool(t, fg)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[worktree: created ") {
		t.Errorf("missing wire-format prefix; got:\n%s", out)
	}
	for _, frag := range []string{"path=", "branch=seek/wt/", "base=abc123"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in:\n%s", frag, out)
		}
	}
}

// TestExecute_PassesArgsThroughToManager: supplied branch + base
// land in the rev-parse + worktree-add calls.
func TestExecute_PassesArgsThroughToManager(t *testing.T) {
	fg := (&fakeGit{}).
		push("def456\n", "", nil).
		push("", "", nil).
		push("", "", nil)
	tl := newTool(t, fg)

	_, err := tl.Execute(context.Background(), json.RawMessage(`{"branch":"feat/x","base":"main"}`))
	if err != nil {
		t.Fatal(err)
	}
	// First call is rev-parse <base>; last arg is the base.
	first := fg.calls[0]
	if first[len(first)-1] != "main" {
		t.Errorf("rev-parse target = %q, want main", first[len(first)-1])
	}
	// Second call is worktree add -b <branch> <path> <sha>;
	// args[2]=="-b", args[3]==branch.
	second := fg.calls[1]
	if len(second) < 5 || second[2] != "-b" || second[3] != "feat/x" {
		t.Errorf("worktree-add args = %v, want `worktree add -b feat/x ...`", second)
	}
}

// TestExecute_ManagerFailureReturnsWireFailure: a Manager.Create
// error surfaces as [worktree: failed reason=create_error] —
// model can read this, not a Go err return.
func TestExecute_ManagerFailureReturnsWireFailure(t *testing.T) {
	fg := (&fakeGit{}).push("", "fatal: not a git repo", errors.New("exit 128"))
	tl := newTool(t, fg)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned err instead of wire failure: %v", err)
	}
	if !strings.HasPrefix(out, "[worktree: failed reason=create_error]") {
		t.Errorf("expected wire-format failure; got:\n%s", out)
	}
	if !strings.Contains(out, "not a git repo") {
		t.Errorf("git stderr should propagate to hint: %s", out)
	}
}

// TestExecute_RejectsUnknownField: strict unmarshal catches typos.
func TestExecute_RejectsUnknownField(t *testing.T) {
	tl := newTool(t, &fakeGit{})
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"branch":"x","bogus":1}`))
	if err == nil {
		t.Fatal("expected strict-unmarshal error")
	}
	if !strings.Contains(err.Error(), "enter_worktree") {
		t.Errorf("error should name the tool: %v", err)
	}
}

func TestNew_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("New(nil) must panic")
		}
	}()
	_ = New(nil)
}
