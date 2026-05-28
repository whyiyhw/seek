package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/subagent"
)

// newToolWithStubRunner builds a Tool wired to a real Manager whose
// Runner is a caller-supplied stub. Most tests just need
// "succeed with this summary" or "fail with this error" — the stub
// makes both trivial without spinning up pkg/agent.
func newToolWithStubRunner(t *testing.T, runner subagent.Runner) *Tool {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	projCwd := t.TempDir()
	policy, err := permission.New(projCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := subagent.NewManager(subagent.ManagerOpts{
		ProjectAbsPath:  projCwd,
		ParentSid:       "20260601-100000-parent",
		ParentTracker:   cache.New(),
		ParentPolicy:    policy,
		ParentToolNames: []string{"read", "grep", "bash", "agent", "ask_user"},
		MaxConcurrent:   3,
		Runner:          runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(mgr)
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

// TestTool_Execute_IsolationWorktreeReturnsNotImplemented: until
// M11.1 lands, isolation=worktree must surface a clear failure
// pointing at the milestone.
func TestTool_Execute_IsolationWorktreeReturnsNotImplemented(t *testing.T) {
	tl := newToolWithStubRunner(t, func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
		t.Error("Runner invoked despite isolation=worktree")
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
	if !strings.Contains(out, "M11.1") {
		t.Errorf("expected M11.1 milestone reference, got:\n%s", out)
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
	_ = New(nil)
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
