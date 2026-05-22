package skilltool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillstats"
)

// recorder is an in-memory statsAppender for tests. Captures every
// Append call so assertions don't need to read a real JSONL file.
type recorder struct {
	mu       sync.Mutex
	entries  []skillstats.Entry
	failNext bool // when true, the next Append returns an error
}

func (r *recorder) Append(e skillstats.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext {
		r.failNext = false
		return errors.New("simulated stats failure")
	}
	r.entries = append(r.entries, e)
	return nil
}

func (r *recorder) snapshot() []skillstats.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]skillstats.Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

func TestStats_SuccessfulCallAppendsRow(t *testing.T) {
	set := newSet(t, &skill.Skill{
		Name: "alpha", Description: "a", Body: "body", Source: "x",
	})
	rec := &recorder{}
	tool := NewWithStats(set, rec, func() Env {
		return Env{
			SessionID: "sess-1",
			ProjectID: "proj-abc",
			Model:     "deepseek-chat",
			Provider:  "deepseek",
		}
	})

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"alpha"}`)); err != nil {
		t.Fatal(err)
	}
	entries := rec.snapshot()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Name != "alpha" {
		t.Errorf("Name = %q", e.Name)
	}
	if e.SessionID != "sess-1" || e.ProjectID != "proj-abc" {
		t.Errorf("session/project ids not propagated: %+v", e)
	}
	if e.Model != "deepseek-chat" || e.Provider != "deepseek" {
		t.Errorf("model/provider not propagated: %+v", e)
	}
	if e.TS == "" {
		t.Errorf("ts not set")
	}
}

func TestStats_FailedCallDoesNotAppend(t *testing.T) {
	// PRD v2 §4.3 — only successful body retrievals count. A
	// missing-name failure must not generate a stats row.
	set := newSet(t, &skill.Skill{Name: "alpha", Description: "a"})
	rec := &recorder{}
	tool := NewWithStats(set, rec, func() Env { return Env{} })

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"missing"}`))
	if err == nil {
		t.Fatal("expected missing-name error")
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("got %d stats rows after failed call, want 0", got)
	}
}

func TestStats_AppendErrorDoesNotBreakToolCall(t *testing.T) {
	// If the stats file is full / permissions broken / etc., the
	// user-facing tool call must still return the body. Stats are
	// best-effort observability, never load-bearing.
	set := newSet(t, &skill.Skill{Name: "alpha", Description: "a", Body: "ok", Source: "x"})
	rec := &recorder{failNext: true}
	tool := NewWithStats(set, rec, func() Env { return Env{SessionID: "s"} })

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"alpha"}`))
	if err != nil {
		t.Fatalf("stats failure leaked to caller: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("body missing: %q", out)
	}
}

func TestStats_NilStatsOrEnvDisablesRecording(t *testing.T) {
	// Backward-compat: callers that constructed Tools via the old
	// New(set) must keep working. Confirmed by both branches of
	// the nil guard.
	set := newSet(t, &skill.Skill{Name: "alpha", Description: "a", Body: "x", Source: "y"})

	// Pure-old path.
	tool := New(set)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"alpha"}`)); err != nil {
		t.Fatal(err)
	}

	// New constructor but nil deps — should be the same as the old path.
	tool2 := NewWithStats(set, nil, nil)
	if _, err := tool2.Execute(context.Background(), json.RawMessage(`{"name":"alpha"}`)); err != nil {
		t.Fatal(err)
	}
}
