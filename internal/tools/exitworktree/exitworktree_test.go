package exitworktree

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/worktree"
)

// TestFormatResult covers the three Status branches plus the
// unknown-status defensive fallback. Tests the wire format
// without needing a Manager — formatResult is a function for
// exactly this reason.
func TestFormatResult(t *testing.T) {
	cases := []struct {
		name string
		in   worktree.CleanupResult
		want string
	}{
		{
			name: "cleaned",
			in:   worktree.CleanupResult{Status: "cleaned"},
			want: "[worktree: cleaned]",
		},
		{
			name: "kept dirty",
			in:   worktree.CleanupResult{Status: "kept", Path: "/p/wt", Branch: "seek/wt/abc", Changes: 3},
			want: "[worktree: kept path=/p/wt branch=seek/wt/abc changes=3]",
		},
		{
			name: "discarded with rescue stash",
			in:   worktree.CleanupResult{Status: "discarded", Changes: 5, StashRef: "refs/seek/discarded/20260601-103412"},
			want: "[worktree: discarded changes=5] rescue stash at refs/seek/discarded/20260601-103412",
		},
		{
			name: "discarded clean (no stash ref)",
			in:   worktree.CleanupResult{Status: "discarded", Changes: 0},
			want: "[worktree: discarded changes=0]",
		},
		{
			name: "unknown status defensive",
			in:   worktree.CleanupResult{Status: "weird"},
			want: `[worktree: failed reason=unknown_status] cleanup returned status="weird"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatResult(c.in)
			if got != c.want {
				t.Errorf("formatResult(%+v)\n got: %s\nwant: %s", c.in, got, c.want)
			}
		})
	}
}

// TestFormatResult_PrefixContract pins the canonical bracketed
// prefix for each Status — these are the wire-format contract.
// If a future change moves the prefix bytes around, the test
// fires and the diff makes the intent auditable.
func TestFormatResult_PrefixContract(t *testing.T) {
	cases := map[string]string{
		"cleaned":   "[worktree: cleaned]",
		"kept":      "[worktree: kept ",
		"discarded": "[worktree: discarded ",
	}
	for status, prefix := range cases {
		out := formatResult(worktree.CleanupResult{Status: status, Path: "/x", Branch: "b", Changes: 1, StashRef: "refs/seek/discarded/r"})
		if !strings.HasPrefix(out, prefix) {
			t.Errorf("status=%s prefix mismatch\n got: %s\nwant prefix: %s", status, out, prefix)
		}
	}
}

// TestExecute_MissingPath: path is required; missing yields
// MissingField error, not a wire failure (the model should fix
// its args, not retry).
func TestExecute_MissingPath(t *testing.T) {
	tl := newTool(t)
	_, err := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Errorf("error should mention path: %v", err)
	}
}

// TestExecute_RejectsUnknownField: strict unmarshal catches typos.
func TestExecute_RejectsUnknownField(t *testing.T) {
	tl := newTool(t)
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"path":"/x","keep_dirty":true}`))
	if err == nil {
		t.Fatal("expected strict-unmarshal error")
	}
}

// TestSchemaIdempotent — same byte stability check as enter side.
func TestSchemaIdempotent(t *testing.T) {
	tl := newTool(t)
	a, b := tl.Schema(), tl.Schema()
	if string(a) != string(b) {
		t.Error("Schema() not idempotent")
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

// newTool builds a tool wired to a Manager whose GitRunner always
// errors — most exit_worktree tests exercise paths that don't
// reach git (arg parsing) or expect wire-format failure. Tests
// that need a specific Manager behaviour script their own.
func newTool(t *testing.T) *Tool {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	mgr, err := worktree.NewManagerWithRunner(t.TempDir(), func(ctx context.Context, cwd string, args ...string) (string, string, error) {
		return "", "stub: git not wired for this test", context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(mgr)
}
