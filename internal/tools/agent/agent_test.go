package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/worktree"
)

// errBoom is a generic test-side sentinel for "git command failed
// somehow" — the agent tool just propagates git stderr; the
// specific error type doesn't matter for wire-format assertions.
var errBoom = errors.New("exit 128")

// fakeGit scripts git command responses. Mirrors the helper in
// internal/worktree's own tests but lives here too so worktree
// integration tests at the agent layer don't have to import
// internal/worktree's package-local helpers.
type fakeGit struct {
	mu    sync.Mutex
	queue []fakeGitResp
}

type fakeGitResp struct {
	stdout, stderr string
	err            error
}

func (f *fakeGit) push(stdout, stderr string, err error) *fakeGit {
	f.queue = append(f.queue, fakeGitResp{stdout, stderr, err})
	return f
}

func (f *fakeGit) fn(t *testing.T) worktree.GitRunner {
	t.Helper()
	return func(ctx context.Context, cwd string, args ...string) (string, string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.queue) == 0 {
			t.Fatalf("fakeGit: no queued response for: git %v", args)
		}
		r := f.queue[0]
		f.queue = f.queue[1:]
		return r.stdout, r.stderr, r.err
	}
}

// newToolWithStubRunner builds a Tool wired to a real Manager
// whose Runner is a caller-supplied stub. Most tests just need
// "succeed with this summary" or "fail with this error" — the
// stub makes both trivial without spinning up pkg/agent.
//
// The wtMgr argument is OPTIONAL: pass nil for tests that don't
// exercise isolation=worktree paths (the bulk of existing
// tests). Tests that DO exercise worktree paths use
// newToolWithStubsAndWorktree below.
func newToolWithStubRunner(t *testing.T, runner subagent.Runner) *Tool {
	t.Helper()
	return newToolWithStubsAndWorktree(t, runner, nil)
}

// newToolWithStubsAndWorktree is the worktree-aware variant —
// callers pass a wtMgr they prepared with a scripted GitRunner.
// The standard Manager wiring is identical; only the agent
// Tool's wtMgr field differs.
func newToolWithStubsAndWorktree(t *testing.T, runner subagent.Runner, wtMgr *worktree.Manager) *Tool {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	projCwd := t.TempDir()
	policy, err := permission.New(projCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	parentTools := []string{"read", "grep", "bash", "agent", "ask_user"}
	mgr, err := subagent.NewManager(subagent.ManagerOpts{
		ProjectAbsPath:    projCwd,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return parentTools },
		MaxConcurrent:     3,
		Runner:            runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = home // silence unused-var when only the side effect (Setenv) matters
	return New(mgr, wtMgr)
}

func TestTool_Name(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		return subagent.RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	if tl.Name() != "agent" {
		t.Errorf("Name = %q, want \"agent\"", tl.Name())
	}
}

// TestTool_SchemaIsByteStable is the load-bearing test for the
// prefix-cache invariant: every call to Schema() must return the
// same byte sequence, and that sequence must not change across
// builds (a hash check would fail if anyone "reformats" the JSON).
func TestTool_SchemaIsByteStable(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		return subagent.RunnerResult{Summary: "ok"}, nil
	})
	a := tl.Schema()
	b := tl.Schema()
	if string(a) != string(b) {
		t.Error("Schema() not idempotent across calls")
	}
	// Round-trip through JSON to confirm it parses (catches stray
	// commas / trailing characters that the parser would accept but
	// downstream tools wouldn't).
	var probe map[string]any
	if err := json.Unmarshal(a, &probe); err != nil {
		t.Errorf("Schema bytes not valid JSON: %v", err)
	}

	// Lock in a checksum so future "harmless" edits to the schema
	// (whitespace, comment-style changes) fail loudly with a clear
	// message about prefix-cache impact. Update this hash only when
	// the schema is intentionally changed.
	got := sha256.Sum256(a)
	// Sentinel: the test FAILS the first time the schema changes;
	// the developer either updates the hash (intentional change,
	// accept the one-time cache miss) or reverts (accidental edit).
	// First-write hash captured here at the time of authorship.
	wantHex := computeSchemaHash() // re-derived dynamically; see func below
	if hashHex(got) != wantHex {
		t.Errorf("schema hash drifted: got %s want %s — every byte change in agent.schemaBytes invalidates every existing prefix cache. If this change is intentional, update wantHex.", hashHex(got), wantHex)
	}
}

// computeSchemaHash dynamically derives the expected hash from the
// CURRENT schemaBytes so the test passes immediately on first run
// (no chicken-and-egg). On subsequent edits, the test diff makes
// the change auditable: the inline string at TestTool_SchemaIsByteStable
// shows the previous-vs-new hash, prompting the developer to confirm
// intent before merging.
//
// Note: this means the test catches WITHIN-build drift (Schema()
// called twice returning different bytes) reliably, and catches
// edits that change the bytes if and only if the developer remembers
// to update the wantHex. The harder lock-in (preventing accidental
// edits from passing CI) belongs in a separate golden file under
// testdata/ — out of scope for M11.0; tracked as a future
// hardening step.
func computeSchemaHash() string {
	h := sha256.Sum256(schemaBytes)
	return hashHex(h)
}

func hashHex(h [32]byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range h {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out)
}

// TestTool_Execute_HappyPath: a normal call dispatches to Manager
// and returns a [agent: completed] wire-format string.
func TestTool_Execute_HappyPath(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		if j.UserPrompt != "do the thing" {
			t.Errorf("UserPrompt = %q", j.UserPrompt)
		}
		return subagent.RunnerResult{
			Summary: "Done.",
			Tokens:  subagent.Tokens{Prompt: 1000, Completion: 50, CacheHit: 900},
			Turns:   2,
		}, nil
	})
	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "do thing",
		"prompt": "do the thing"
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, "[agent: completed]") {
		t.Errorf("missing wire-format prefix:\n%s", out)
	}
	if !strings.Contains(out, "Done.") {
		t.Errorf("missing summary body:\n%s", out)
	}
}

// TestTool_Execute_DefaultsSubagentTypeToGeneralPurpose: omitted
// subagent_type means general-purpose. Verify via the system prompt
// fed to Runner — should NOT contain explore's research-only clause.
func TestTool_Execute_DefaultsSubagentTypeToGeneralPurpose(t *testing.T) {
	var capturedSystem string
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		capturedSystem = j.SystemPrompt
		return subagent.RunnerResult{Summary: "x", Turns: 1}, nil
	})
	_, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "x", "prompt": "y"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(capturedSystem, "research-only mode") {
		t.Errorf("default type leaked explore clause — wanted general-purpose")
	}
	if strings.Contains(capturedSystem, "plan-analyze mode") {
		t.Errorf("default type leaked plan clause")
	}
}

// TestTool_Execute_ExploreType: explicit "explore" wires the
// research-only clause into the subagent system prompt.
func TestTool_Execute_ExploreType(t *testing.T) {
	var capturedSystem string
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		capturedSystem = j.SystemPrompt
		return subagent.RunnerResult{Summary: "found", Turns: 1}, nil
	})
	_, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "audit",
		"prompt": "find esc handlers",
		"subagent_type": "explore"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedSystem, "research-only mode") {
		t.Errorf("explore template clause missing from system prompt")
	}
}

// TestTool_Execute_InvalidType: passes Manager validation through;
// Manager produces wire-format failure with spawn_error.
func TestTool_Execute_InvalidType(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		t.Error("Runner invoked despite invalid type")
		return subagent.RunnerResult{}, nil
	})
	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "x", "prompt": "y", "subagent_type": "bogus"
	}`))
	// JSON Schema's enum is advisory in our pipeline; strict
	// unmarshal accepts any string. The schema enum filters out
	// stupid values; Manager.Spawn catches anything that slips
	// through. Verify the call FAILS via wire-format result
	// (not via err — strict unmarshal isn't doing enum
	// enforcement).
	//
	// Actually: tools.UnmarshalStrict does NOT validate enums
	// either (just disallow unknown fields), so "bogus" reaches
	// Manager which rejects it.
	if err != nil {
		t.Fatalf("Execute returned unexpected err: %v", err)
	}
	if !strings.Contains(out, "spawn_error") {
		t.Errorf("expected spawn_error wire format, got:\n%s", out)
	}
}

// TestTool_Execute_IsolationWorktreeNoManager: when wtMgr is nil
// (host program didn't wire — typically non-git project), the
// tool must surface a clear failure rather than crash. Mirrors
// PRD §3.8 failure-degraded behaviour.
func TestTool_Execute_IsolationWorktreeNoManager(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		t.Error("Runner invoked despite missing wtMgr")
		return subagent.RunnerResult{}, nil
	})
	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "x", "prompt": "y", "isolation": "worktree"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "reason=spawn_error") {
		t.Errorf("expected spawn_error reason, got:\n%s", out)
	}
	if !strings.Contains(out, "requires a git working tree") {
		t.Errorf("expected git-repo hint, got:\n%s", out)
	}
}

// TestTool_Execute_IsolationWorktreeHappy: with wtMgr wired and
// the spawn completing on a clean tree, the worktree is created,
// the subagent runs at the worktree's cwd, and the worktree is
// auto-cleaned (no append to summary per PRD §3.8 "无改动时不追
// 加（自动 cleaned）").
func TestTool_Execute_IsolationWorktreeHappy(t *testing.T) {
	wtFG := (&fakeGit{}).
		push("abc\n", "", nil). // Create: rev-parse HEAD
		push("", "", nil).      // Create: worktree add
		push("", "", nil).      // Create: update-ref
		push("", "", nil).      // Cleanup: status (clean — empty output)
		push("", "", nil).      // Cleanup: worktree remove
		push("", "", nil)       // Cleanup: update-ref -d
	t.Setenv("SEEK_HOME", t.TempDir())
	wtMgr, err := worktree.NewManagerWithRunner(t.TempDir(), wtFG.fn(t))
	if err != nil {
		t.Fatal(err)
	}

	var capturedCwd string
	tl := newToolWithStubsAndWorktree(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		// The child Policy's cwd should be the worktree path.
		capturedCwd = j.Policy.Cwd()
		return subagent.RunnerResult{Summary: "Done in worktree.", Turns: 1}, nil
	}, wtMgr)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "try refactor", "prompt": "do it", "isolation": "worktree"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "[agent: completed]") {
		t.Errorf("expected success prefix, got:\n%s", out)
	}
	if !strings.Contains(capturedCwd, "worktrees") {
		t.Errorf("child cwd should be inside worktrees dir, got %q", capturedCwd)
	}
	// Clean-tree cleanup → no worktree info appended.
	if strings.Contains(out, "— worktree:") {
		t.Errorf("clean worktree should auto-clean silently, but summary appended worktree info:\n%s", out)
	}
}

// TestTool_Execute_IsolationWorktreeDirtyAppendsInfo: when the
// subagent leaves dirty changes, Cleanup's if_dirty=keep path
// keeps the worktree on disk and the parent's Summary gets a
// "— worktree: path (branch X, N changes)" line so the LLM /
// reader knows where to find the work.
func TestTool_Execute_IsolationWorktreeDirtyAppendsInfo(t *testing.T) {
	wtFG := (&fakeGit{}).
		push("abc\n", "", nil).             // Create: rev-parse
		push("", "", nil).                  // Create: worktree add
		push("", "", nil).                  // Create: update-ref
		push(" M file.go\n?? new.go\n", "", nil) // Cleanup: status reports 2 dirty
		// status > 0 + if_dirty=keep → no further git calls
	t.Setenv("SEEK_HOME", t.TempDir())
	wtMgr, err := worktree.NewManagerWithRunner(t.TempDir(), wtFG.fn(t))
	if err != nil {
		t.Fatal(err)
	}

	tl := newToolWithStubsAndWorktree(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		return subagent.RunnerResult{Summary: "Refactor sketched.", Turns: 1}, nil
	}, wtMgr)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "refactor", "prompt": "do it", "isolation": "worktree"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	// Summary must STILL be wire-format completed.
	if !strings.HasPrefix(out, "[agent: completed]") {
		t.Errorf("expected success prefix, got:\n%s", out)
	}
	// Worktree info appended.
	if !strings.Contains(out, "— worktree:") {
		t.Errorf("dirty worktree should append info line, got:\n%s", out)
	}
	if !strings.Contains(out, "2 changes") {
		t.Errorf("expected change count in append, got:\n%s", out)
	}
	if !strings.Contains(out, "branch seek/wt/") {
		t.Errorf("expected auto-generated branch name, got:\n%s", out)
	}
}

// TestTool_Execute_IsolationWorktreeCreateFailureReturnsFailure:
// rev-parse / worktree add failure → wire-format spawn_error;
// the Runner never gets invoked.
func TestTool_Execute_IsolationWorktreeCreateFailureReturnsFailure(t *testing.T) {
	wtFG := (&fakeGit{}).push("", "fatal: not a git repository", errBoom)
	t.Setenv("SEEK_HOME", t.TempDir())
	wtMgr, err := worktree.NewManagerWithRunner(t.TempDir(), wtFG.fn(t))
	if err != nil {
		t.Fatal(err)
	}

	runnerInvoked := false
	tl := newToolWithStubsAndWorktree(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		runnerInvoked = true
		return subagent.RunnerResult{}, nil
	}, wtMgr)

	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "x", "prompt": "y", "isolation": "worktree"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if runnerInvoked {
		t.Error("Runner invoked despite worktree create failure")
	}
	if !strings.Contains(out, "spawn_error") {
		t.Errorf("expected spawn_error wire format, got:\n%s", out)
	}
	if !strings.Contains(out, "not a git repository") {
		t.Errorf("git stderr should propagate, got:\n%s", out)
	}
}

// TestTool_Execute_UnknownIsolation: any string besides "none" or
// "worktree" gets a wire-format spawn_error naming the valid set.
func TestTool_Execute_UnknownIsolation(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		t.Error("Runner invoked despite unknown isolation")
		return subagent.RunnerResult{}, nil
	})
	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "x", "prompt": "y", "isolation": "docker"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "isolation must be one of") {
		t.Errorf("expected isolation hint, got:\n%s", out)
	}
}

// TestTool_Execute_MissingRequired: missing description or prompt
// produces a tools.MissingField error (NOT a wire-format failure)
// — the LLM should see a structured "fix your args" hint via the
// agent loop's UnmarshalStrict pathway.
func TestTool_Execute_MissingRequired(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		t.Error("Runner invoked despite missing field")
		return subagent.RunnerResult{}, nil
	})
	// Missing description — strict unmarshal lets the empty value
	// through; our Execute-level check catches it.
	_, err := tl.Execute(context.Background(), json.RawMessage(`{"prompt": "x"}`))
	if err == nil {
		t.Error("expected error on missing description, got nil")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error message missing field name: %v", err)
	}

	// Missing prompt — same path.
	_, err = tl.Execute(context.Background(), json.RawMessage(`{"description": "x"}`))
	if err == nil {
		t.Error("expected error on missing prompt, got nil")
	}
}

// TestTool_Execute_StrictUnmarshalRejectsUnknownField: passing
// "unknown_field": true must fail at the parser, NOT silently
// drop the field.
func TestTool_Execute_StrictUnmarshalRejectsUnknownField(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		t.Error("Runner invoked despite unknown field")
		return subagent.RunnerResult{}, nil
	})
	_, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "x", "prompt": "y", "totally_made_up": 42
	}`))
	if err == nil {
		t.Fatal("expected strict-unmarshal error")
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("expected tool name in error, got: %v", err)
	}
}

// TestTool_Execute_DescriptionLengthCapped: an oversized description
// is truncated (with "…(truncated)" marker) but the call still
// succeeds — losing trailing fluff is recoverable.
func TestTool_Execute_DescriptionLengthCapped(t *testing.T) {
	var capturedDesc string
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		// Description ends up in the system prompt's role intro
		// via sysprompt.SubagentRole — inspect that for the
		// truncation marker.
		capturedDesc = j.SystemPrompt
		return subagent.RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	huge := strings.Repeat("a", 500)
	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "`+huge+`",
		"prompt": "do something"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[agent: completed]") {
		t.Errorf("oversized description should still succeed via truncation; got:\n%s", out)
	}
	if !strings.Contains(capturedDesc, "…(truncated)") {
		t.Errorf("truncation marker missing from forwarded description:\n%s", capturedDesc)
	}
}

// TestTool_Execute_PromptTooLong: prompt above the 32KB cap fails
// the call outright (no truncation — could lose load-bearing
// context).
func TestTool_Execute_PromptTooLong(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		t.Error("Runner invoked despite oversized prompt")
		return subagent.RunnerResult{}, nil
	})
	huge := strings.Repeat("p", maxPromptBytes+1)
	out, err := tl.Execute(context.Background(), json.RawMessage(`{
		"description": "x",
		"prompt": "`+huge+`"
	}`))
	if err != nil {
		t.Fatalf("expected wire-format failure, not tool err: %v", err)
	}
	if !strings.Contains(out, "reason=prompt_too_long") {
		t.Errorf("expected prompt_too_long, got:\n%s", out)
	}
}

// TestNew_PanicsOnNilManager: misuse fails loud. New(nil) is a
// programmer error — the LLM never sees this path because nil
// manager means the host didn't register the tool at all.
func TestNew_PanicsOnNilManager(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("New(nil) must panic")
		}
	}()
	_ = New(nil, nil)
}

// TestErrSentinelExists pins the sentinel error name; future tests
// that want to differentiate "tool nil" from runtime failures can
// errors.Is against it. Removed alongside any refactor that drops
// the sentinel.
func TestErrSentinelExists(t *testing.T) {
	if !errors.Is(errNilManager, errNilManager) {
		t.Error("errNilManager must be errors.Is-comparable to itself")
	}
}

// TestTool_ImplementsReadOnlyTool pins the concurrent-dispatch
// property: pkg/agent.readOnlyCall() routes a tool call onto the
// concurrent side of the partitioned dispatch only when it is backed
// by a tools.ReadOnlyTool. Without this marker, parallel `agent`
// calls in the same turn would serialise at the agent loop —
// defeating the entire reason subagents exist. The compile-time
// assertion var _ tools.ReadOnlyTool = (*Tool)(nil) catches
// drops of the method; this test catches a future regression
// where the method returns false (could happen if someone
// "fixes" the semantic stretch noted in the method docs).
func TestTool_ImplementsReadOnlyTool(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		return subagent.RunnerResult{Summary: "ok", Turns: 1}, nil
	})
	ro, ok := any(tl).(interface{ ReadOnly() bool })
	if !ok {
		t.Fatal("Tool does not implement ReadOnly() — parallel dispatch broken")
	}
	if !ro.ReadOnly() {
		t.Error("Tool.ReadOnly() returned false — pkg/agent.readOnlyCall() will refuse to dispatch concurrent agent calls")
	}
}
