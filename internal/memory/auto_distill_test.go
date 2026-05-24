package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestAutoDistill_GatedOffByDefault(t *testing.T) {
	t.Setenv("SEEK_AUTO_DISTILL", "")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	h := &Hook{
		Project:   p,
		Distiller: &Distiller{Client: &fakeChatClient{}},
	}
	// OnSessionEnd is a no-op in v2.
	h.OnSessionEnd(context.Background(), hooks.SessionEndEvent{})
	if len(p.Entries()) != 0 {
		t.Errorf("v2 OnSessionEnd is no-op; expected 0 entries, got %d", len(p.Entries()))
	}
}

func TestObserveEnqueue_RejectsConfirmedEntry(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	_ = p.Add(Entry{Name: "existing", Tagline: "t", Content: "c", AutoSourced: false})

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `{"decision": "ACCEPT", "reason": "new info"}`,
		}}}},
	}

	resultChan := make(chan ObserveResult, 5)
	h := &Hook{
		Project:    p,
		Distiller:  &Distiller{Client: fake},
		ResultChan: resultChan,
	}

	enqueue := h.ObserveEnqueue()
	enqueue(context.Background(), Entry{Name: "existing", Tagline: "new", Content: "should be rejected"})

	result := <-resultChan
	if result.OK {
		t.Errorf("expected rejection for confirmed entry, got OK=true")
	}
	if result.Err == "" {
		t.Errorf("expected error message for confirmed entry rejection")
	}
}

func TestObserveEnqueue_WritesAutoSourcedOnAccept(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `{"decision": "ACCEPT", "reason": "new project decision"}`,
		}}}},
	}

	resultChan := make(chan ObserveResult, 5)
	h := &Hook{
		Project:    p,
		Distiller:  &Distiller{Client: fake},
		ResultChan: resultChan,
	}

	enqueue := h.ObserveEnqueue()
	enqueue(context.Background(), Entry{Name: "new-decision", Tagline: "new decision", Content: "detailed rationale"})

	result := <-resultChan
	if !result.OK {
		t.Errorf("expected ACCEPT, got OK=false err=%q", result.Err)
	}

	entry, ok := p.Get("new-decision")
	if !ok {
		t.Fatal("expected entry to be written to M")
	}
	if !entry.AutoSourced {
		t.Errorf("entry should have AutoSourced=true")
	}
	if entry.ObserveCount != 1 {
		t.Errorf("first observe should have ObserveCount=1, got %d", entry.ObserveCount)
	}
}

func TestObserveEnqueue_RespectsSessionCap(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `{"decision": "ACCEPT", "reason": "new info"}`,
		}}}},
	}

	resultChan := make(chan ObserveResult, 20)
	h := &Hook{
		Project:    p,
		Distiller:  &Distiller{Client: fake},
		ResultChan: resultChan,
		observeMax: 2,
	}

	enqueue := h.ObserveEnqueue()

	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("entry-%d", i)
		enqueue(context.Background(), Entry{Name: name, Tagline: name, Content: name})
		r := <-resultChan
		if !r.OK {
			t.Errorf("entry %q should be accepted", name)
		}
	}

	// Third call: over cap, silent discard.
	enqueue(context.Background(), Entry{Name: "over-cap", Tagline: "x", Content: "x"})
	select {
	case r := <-resultChan:
		t.Errorf("over-cap entry should not produce a result, got %+v", r)
	default:
	}
}

func TestAutoDistill_NilDistillerOrHistorySafety(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	// Missing Distiller: ObserveEnqueue returns nil.
	h := &Hook{Project: p}
	if fn := h.ObserveEnqueue(); fn != nil {
		t.Errorf("ObserveEnqueue should return nil when Distiller is nil")
	}
}

func TestObserveEnqueue_OverwritesUnconfirmedEntry(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	// Plant an existing auto_sourced entry (unconfirmed).
	old := time.Now().UTC().Add(-24 * time.Hour)
	plantEntry(t, p, Entry{
		Name:           "auto-decision",
		Tagline:        "old title",
		Content:        "old content",
		CreatedAt:      old,
		UpdatedAt:      old,
		LastRecalledAt: old,
		AutoSourced:    true,
	})

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `{"decision": "ACCEPT", "reason": "updated rationale"}`,
		}}}},
	}

	resultChan := make(chan ObserveResult, 5)
	h := &Hook{
		Project:    p,
		Distiller:  &Distiller{Client: fake},
		ResultChan: resultChan,
	}

	enqueue := h.ObserveEnqueue()
	enqueue(context.Background(), Entry{Name: "auto-decision", Tagline: "updated title", Content: "updated content"})

	result := <-resultChan
	if !result.OK {
		t.Fatalf("expected ACCEPT for overwriting unconfirmed entry, got OK=false err=%q", result.Err)
	}

	got, ok := p.Get("auto-decision")
	if !ok {
		t.Fatal("entry should still exist after overwrite")
	}
	if got.Tagline != "updated title" {
		t.Errorf("tagline should be updated, got %q", got.Tagline)
	}
	if got.Content != "updated content" {
		t.Errorf("content should be updated, got %q", got.Content)
	}
	if !got.AutoSourced {
		t.Errorf("entry should remain AutoSourced=true")
	}
	if got.UpdatedAt.Equal(old) {
		t.Errorf("UpdatedAt should be refreshed after overwrite")
	}
	// After overwriting an auto_sourced entry, ObserveCount is incremented.
	if got.ObserveCount < 1 {
		t.Errorf("ObserveCount should be ≥1 after overwrite, got %d", got.ObserveCount)
	}
}

func TestObserveEnqueue_PerNameDedup(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	block := make(chan struct{})
	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `{"decision": "ACCEPT", "reason": "new info"}`,
		}}}},
		block: block,
	}

	resultChan := make(chan ObserveResult, 5)
	h := &Hook{
		Project:    p,
		Distiller:  &Distiller{Client: fake},
		ResultChan: resultChan,
		observeMax: 10,
	}

	enqueue := h.ObserveEnqueue()

	// First call: launches a goroutine that blocks in fake.Chat()
	// (because block channel is not yet closed). Per-name lock is held
	// for the duration of the block.
	enqueue(context.Background(), Entry{Name: "dedup-name", Tagline: "first", Content: "first call"})

	// Second call with same name: per-name lock is still held by the
	// blocked goroutine → tryLock fails → silent merge, no goroutine.
	enqueue(context.Background(), Entry{Name: "dedup-name", Tagline: "second", Content: "second call"})

	// Release the first goroutine so it can complete.
	close(block)

	// Only one result should arrive (first goroutine).
	result := <-resultChan
	if !result.OK {
		t.Errorf("first goroutine should have written, got OK=false err=%q", result.Err)
	}
	if result.Tagline != "first" {
		t.Errorf("should be the first call's result, got tagline=%q", result.Tagline)
	}

	// No second result — the second call was silently merged.
	select {
	case r := <-resultChan:
		t.Errorf("second call should not produce a result, got %+v", r)
	default:
	}
}

func TestObserve_FilterParse(t *testing.T) {
	tests := []struct {
		raw     string
		want    FilterResult
		wantErr bool
	}{
		{`{"decision": "ACCEPT", "reason": "good"}`, FilterAccept, false},
		{`{"decision": "REJECT", "reason": "duplicate"}`, FilterReject, false},
		{`garbage`, FilterReject, true},
		{`{}`, FilterReject, true},
	}

	for _, tt := range tests {
		got, _, err := parseFilterResult(tt.raw)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseFilterResult(%q) expected error", tt.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFilterResult(%q) unexpected error: %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseFilterResult(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
