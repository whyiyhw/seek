package memorytool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/permission"
)

// setupProject creates an isolated ~/.seek root + project, returning
// the loaded Project handle. The test's cwd is the project (so the
// permission Policy anchored there approves only that tree).
func setupProject(t *testing.T) *memory.Project {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	cwd := t.TempDir()
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return p
}

func TestRecall_HitReturnsEntryAndBumpsRecall(t *testing.T) {
	p := setupProject(t)
	if err := p.Add(memory.Entry{
		Name:    "session-format",
		Tagline: "JSONL not JSON",
		Content: "rationale...",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	tool := NewRecall(p)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"session-format"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "JSONL not JSON") {
		t.Errorf("output should include tagline, got %q", out)
	}

	// Reload to confirm the bump was persisted, not just in-memory.
	got, ok := p.Get("session-format")
	if !ok {
		t.Fatal("entry vanished")
	}
	if got.RecallCount != 1 {
		t.Errorf("RecallCount = %d, want 1", got.RecallCount)
	}
	if got.LastRecalledAt.Equal(got.CreatedAt) {
		t.Errorf("LastRecalledAt should advance past CreatedAt after Recall")
	}
}

func TestRecall_MissReturnsHelpfulMessage(t *testing.T) {
	p := setupProject(t)
	tool := NewRecall(p)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "no entry") || !strings.Contains(out, "nope") {
		t.Errorf("miss message should name the key, got %q", out)
	}
}

func TestRecall_NilProject_TellsModel(t *testing.T) {
	tool := NewRecall(nil)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "not available") {
		t.Errorf("nil-project message should say memory is unavailable, got %q", out)
	}
}

func TestRecall_ReadOnlyInterface(t *testing.T) {
	// Recall participates in the agent's parallel read-only dispatch
	// optimisation. Asserting the interface here protects against an
	// accidental signature change silently demoting it.
	var _ interface{ ReadOnly() bool } = Recall{}
	if !(Recall{}).ReadOnly() {
		t.Errorf("Recall.ReadOnly() should be true")
	}
}

func TestRecall_RejectsExtraFields(t *testing.T) {
	tool := NewRecall(setupProject(t))
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"x","extra":"oops"}`))
	if err == nil {
		t.Errorf("expected strict unmarshal to reject unknown field")
	}
}

func TestRemember_AskApprovedWrites(t *testing.T) {
	p := setupProject(t)
	policy, err := permission.New(t.TempDir(), permission.ModeAsk)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	var saw permission.Action
	policy.SetAskFn(func(a permission.Action) bool {
		saw = a
		return true
	})

	tool := NewRemember(p, policy)
	args := json.RawMessage(`{
		"name": "test-entry",
		"tagline": "one-liner",
		"content": "full rationale here",
		"tags": ["architecture"]
	}`)
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "remembered") {
		t.Errorf("output should confirm save, got %q", out)
	}

	// askFn must have received the metadata the TUI needs.
	if saw.Kind != permission.KindMemoryRemember {
		t.Errorf("Kind = %q, want KindMemoryRemember", saw.Kind)
	}
	if saw.MemoryName != "test-entry" || saw.MemoryTagline != "one-liner" {
		t.Errorf("Action missing memory metadata: %+v", saw)
	}

	// Entry actually persisted.
	got, ok := p.Get("test-entry")
	if !ok {
		t.Fatal("entry not stored after Remember")
	}
	if got.Content != "full rationale here" {
		t.Errorf("Content = %q, want %q", got.Content, "full rationale here")
	}
	if len(got.Tags) != 1 || got.Tags[0] != "architecture" {
		t.Errorf("Tags = %v, want [architecture]", got.Tags)
	}
}

func TestRemember_AskDeniedSkipsWrite(t *testing.T) {
	p := setupProject(t)
	policy, _ := permission.New(t.TempDir(), permission.ModeAsk)
	policy.SetAskFn(func(permission.Action) bool { return false })

	tool := NewRemember(p, policy)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"name": "denied",
		"tagline": "x",
		"content": "y"
	}`))
	if err == nil {
		t.Fatalf("expected ErrDenied")
	}
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("expected permission.ErrDenied, got %v", err)
	}
	if _, ok := p.Get("denied"); ok {
		t.Errorf("denied entry should not have been stored")
	}
}

func TestRemember_YoloModeSkipsAsk(t *testing.T) {
	p := setupProject(t)
	policy, _ := permission.New(t.TempDir(), permission.ModeYolo)
	askCalled := false
	policy.SetAskFn(func(permission.Action) bool {
		askCalled = true
		return true
	})

	tool := NewRemember(p, policy)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"name": "yolo-entry",
		"tagline": "x",
		"content": "y"
	}`))
	if err != nil {
		t.Fatalf("Execute in yolo: %v", err)
	}
	if askCalled {
		t.Errorf("ModeYolo must not consult askFn")
	}
	if _, ok := p.Get("yolo-entry"); !ok {
		t.Errorf("entry should persist in yolo mode")
	}
}

func TestRemember_DenyModeRejectsBeforeAsk(t *testing.T) {
	p := setupProject(t)
	policy, _ := permission.New(t.TempDir(), permission.ModeDeny)
	askCalled := false
	policy.SetAskFn(func(permission.Action) bool {
		askCalled = true
		return true
	})

	tool := NewRemember(p, policy)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"name": "deny-entry",
		"tagline": "x",
		"content": "y"
	}`))
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("expected ErrDenied in ModeDeny, got %v", err)
	}
	if askCalled {
		t.Errorf("ModeDeny must not consult askFn")
	}
}

func TestRemember_MissingRequiredFields(t *testing.T) {
	tool := NewRemember(setupProject(t), mustPolicy(t))
	for _, body := range []string{
		`{"tagline":"t","content":"c"}`,           // no name
		`{"name":"n","content":"c"}`,              // no tagline
		`{"name":"n","tagline":"t"}`,              // no content
		`{"name":"","tagline":"t","content":"c"}`, // empty name
	} {
		_, err := tool.Execute(context.Background(), json.RawMessage(body))
		if err == nil {
			t.Errorf("expected error for %s", body)
		}
	}
}

func TestRemember_NilProjectReturnsClearError(t *testing.T) {
	policy, _ := permission.New(t.TempDir(), permission.ModeYolo)
	tool := NewRemember(nil, policy)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"name":"x","tagline":"t","content":"c"
	}`))
	if err == nil {
		t.Errorf("expected error when project is nil")
	}
}

// TestRemember_RoundTrip_PersistsAcrossReload guards the
// project.Add → file → LoadOrCreate path end-to-end.
func TestRemember_RoundTrip_PersistsAcrossReload(t *testing.T) {
	p := setupProject(t)
	cwd := p.AbsPath
	policy, _ := permission.New(t.TempDir(), permission.ModeYolo)
	tool := NewRemember(p, policy)

	now := time.Now().UTC()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{
		"name":"durable","tagline":"durable","content":"long content"
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	reloaded, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	got, ok := reloaded.Get("durable")
	if !ok {
		t.Fatal("entry missing after reload")
	}
	if got.Content != "long content" {
		t.Errorf("Content = %q, want %q", got.Content, "long content")
	}
	if got.CreatedAt.Before(now.Add(-time.Second)) {
		t.Errorf("CreatedAt should be ~now, got %v", got.CreatedAt)
	}
}

func mustPolicy(t *testing.T) *permission.Policy {
	t.Helper()
	p, err := permission.New(t.TempDir(), permission.ModeYolo)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	return p
}
