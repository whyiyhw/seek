package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestLoadDreamState_MissingFileReturnsZero(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	s, err := LoadDreamState()
	if err != nil {
		t.Fatalf("LoadDreamState: %v", err)
	}
	if s.SchemaVersion != dreamStateSchemaVersion {
		t.Errorf("fresh state should have schema_version=%d, got %d",
			dreamStateSchemaVersion, s.SchemaVersion)
	}
	if !s.LastDreamAt.IsZero() {
		t.Errorf("fresh state LastDreamAt should be zero, got %v", s.LastDreamAt)
	}
	if s.SessionsSinceDream != 0 {
		t.Errorf("fresh state SessionsSinceDream should be 0, got %d", s.SessionsSinceDream)
	}
}

func TestDreamState_SaveRoundTrip(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	s := &DreamState{
		LastDreamAt:        time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		SessionsSinceDream: 7,
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, err := LoadDreamState()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !reloaded.LastDreamAt.Equal(s.LastDreamAt) {
		t.Errorf("LastDreamAt drift: %v → %v", s.LastDreamAt, reloaded.LastDreamAt)
	}
	if reloaded.SessionsSinceDream != 7 {
		t.Errorf("SessionsSinceDream lost: got %d", reloaded.SessionsSinceDream)
	}
}

func TestDreamState_CorruptFileFallsBackToFresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	if err := os.WriteFile(filepath.Join(home, dreamStateFile),
		[]byte("not valid json {"), 0o644); err != nil {
		t.Fatalf("plant junk: %v", err)
	}
	s, err := LoadDreamState()
	if err != nil {
		t.Fatalf("LoadDreamState on corrupt: %v", err)
	}
	if s.SchemaVersion != dreamStateSchemaVersion {
		t.Errorf("corrupt file should reset to fresh, got schema_version=%d", s.SchemaVersion)
	}
}

func TestIsDreamDue_SessionsCap(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_AUTO_DREAM_SESSIONS", "")
	t.Setenv("SEEK_AUTO_DREAM_DAYS", "")
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		sessions int
		wantDue  bool
	}{
		{"under cap", 5, false},
		{"at cap", defaultDreamEverySessions, true},
		{"over cap", defaultDreamEverySessions + 3, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &DreamState{SessionsSinceDream: c.sessions}
			r := s.IsDreamDue(now)
			if r.Due != c.wantDue {
				t.Errorf("sessions=%d → Due=%v, want %v (%+v)", c.sessions, r.Due, c.wantDue, r)
			}
		})
	}
}

func TestIsDreamDue_DaysCap(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		lastDream   time.Time
		wantDue     bool
		wantTrigger bool
	}{
		{"never dreamed", time.Time{}, false, false}, // zero → no days-trigger on day 1
		{"dreamed yesterday", now.Add(-1 * 24 * time.Hour), false, false},
		{"dreamed at day-cap exact", now.Add(-time.Duration(defaultDreamEveryDays) * 24 * time.Hour), true, true},
		{"dreamed long ago", now.Add(-90 * 24 * time.Hour), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &DreamState{LastDreamAt: c.lastDream}
			r := s.IsDreamDue(now)
			if r.Due != c.wantDue {
				t.Errorf("lastDream=%v → Due=%v, want %v (%+v)", c.lastDream, r.Due, c.wantDue, r)
			}
			if r.DaysTrigger != c.wantTrigger {
				t.Errorf("DaysTrigger expected %v, got %v", c.wantTrigger, r.DaysTrigger)
			}
		})
	}
}

func TestIsDreamDue_CustomEnvCaps(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_AUTO_DREAM_SESSIONS", "3")
	t.Setenv("SEEK_AUTO_DREAM_DAYS", "1")

	s := &DreamState{SessionsSinceDream: 3}
	r := s.IsDreamDue(time.Now())
	if !r.Due || r.SessionCap != 3 {
		t.Errorf("env override should set SessionCap=3 and fire at 3 sessions: %+v", r)
	}
}

func TestIsDreamDue_InvalidEnvFallsBack(t *testing.T) {
	t.Setenv("SEEK_AUTO_DREAM_SESSIONS", "garbage")
	t.Setenv("SEEK_AUTO_DREAM_DAYS", "-5")
	s := &DreamState{}
	r := s.IsDreamDue(time.Now())
	if r.SessionCap != defaultDreamEverySessions || r.DayCap != defaultDreamEveryDays {
		t.Errorf("invalid env should fall back to defaults; got %+v", r)
	}
}

func TestAutoDreamEnabled_TruthyValues(t *testing.T) {
	for _, v := range []string{"", "1", "true", "yes", "on", "TRUE", "Yes"} {
		t.Setenv("SEEK_AUTO_DREAM", v)
		if !autoDreamEnabled() {
			t.Errorf("%q should enable", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "off", "maybe"} {
		t.Setenv("SEEK_AUTO_DREAM", v)
		if autoDreamEnabled() {
			t.Errorf("%q should NOT enable", v)
		}
	}
}

// TestAutoDream_SingleProjectNoop verifies that auto-dream doesn't waste
// a reasoner call when there's only one project — the N≥2 filter would
// reject everything anyway. The goroutine still starts (default-on) but
// returns before calling the reasoner.
func TestAutoDream_SingleProjectNoop(t *testing.T) {
	t.Setenv("SEEK_AUTO_DREAM", "1")
	t.Setenv("SEEK_AUTO_DREAM_SESSIONS", "1")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "anchor", Tagline: "x", Content: "y"})

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `[{"trait":"x","why":"y","sources":["a","b"]}]`,
		}}}},
	}
	h := &Hook{
		Project:       p,
		Dreamer:       &Dreamer{Client: fake},
		autoDreamDone: make(chan struct{}),
	}
	h.OnSessionStart(context.Background(), hooks.SessionStartEvent{})

	// Goroutine runs but returns immediately (<2 projects → skip reasoner).
	select {
	case <-h.autoDreamDone:
		// Good: goroutine ran but skipped the reasoner.
	case <-time.After(500 * time.Millisecond):
		t.Error("goroutine should have completed (skipping reasoner)")
	}
	if fake.lastReq != nil {
		t.Errorf("reasoner should NOT be called with <2 projects")
	}
}

func TestAutoDream_TriggersWhenDueAndWritesPending(t *testing.T) {
	t.Setenv("SEEK_AUTO_DREAM", "1")
	t.Setenv("SEEK_AUTO_DREAM_SESSIONS", "1") // every session
	cwd, home := withMemoryEnv(t)

	// Two projects so the ≥2-source filter doesn't fail us.
	pa, _ := LoadOrCreate(cwd)
	_ = pa.Add(Entry{Name: "alpha", Tagline: "a"})
	cwdB := t.TempDir()
	pb, _ := LoadOrCreate(cwdB)
	_ = pb.Add(Entry{Name: "beta", Tagline: "b"})

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `[{"trait":"user prefers terse code","why":"both projects","sources":["` + pa.ID + `","` + pb.ID + `"]}]`,
		}}}},
	}
	done := make(chan struct{})
	h := &Hook{
		Project:       pa,
		Dreamer:       &Dreamer{Client: fake},
		autoDreamDone: done,
	}
	h.OnSessionStart(context.Background(), hooks.SessionStartEvent{})

	select {
	case <-done:
		// Goroutine finished.
	case <-time.After(2 * time.Second):
		t.Fatal("auto-dream goroutine did not complete within 2s")
	}

	// Soul.Pending should now contain the trait.
	soul, _ := LoadSoul()
	if soul.Pending == "" || !contains(soul.Pending, "user prefers terse code") {
		t.Errorf("expected new trait in soul.Pending; got %q", soul.Pending)
	}

	// DreamState advanced: SessionsSinceDream reset, LastDreamAt set.
	state, _ := LoadDreamState()
	if state.SessionsSinceDream != 0 {
		t.Errorf("SessionsSinceDream should reset on successful dream, got %d", state.SessionsSinceDream)
	}
	if state.LastDreamAt.IsZero() {
		t.Errorf("LastDreamAt should be set after dream")
	}
	_ = home // silence unused (we use home implicitly via SEEK_HOME)
}

func TestAutoDream_IncrementsCounterWhenNotDue(t *testing.T) {
	t.Setenv("SEEK_AUTO_DREAM", "1")
	t.Setenv("SEEK_AUTO_DREAM_SESSIONS", "5") // not due at session 1
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	h := &Hook{
		Project: p,
		Dreamer: &Dreamer{Client: &fakeChatClient{}},
	}
	h.OnSessionStart(context.Background(), hooks.SessionStartEvent{})

	state, _ := LoadDreamState()
	if state.SessionsSinceDream != 1 {
		t.Errorf("counter should advance even when not due; got %d", state.SessionsSinceDream)
	}
	if !state.LastDreamAt.IsZero() {
		t.Errorf("LastDreamAt should remain zero when no dream ran")
	}
}

func TestAutoDream_NilDreamerSafe(t *testing.T) {
	t.Setenv("SEEK_AUTO_DREAM", "1")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	h := &Hook{Project: p}
	h.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	// Must not panic; counter must NOT advance because the gate
	// short-circuits before touching DreamState (Dreamer nil → return).
}

func TestAutoDream_NoCandidatesDoesNotWritePending(t *testing.T) {
	t.Setenv("SEEK_AUTO_DREAM", "1")
	t.Setenv("SEEK_AUTO_DREAM_SESSIONS", "1")
	cwd, _ := withMemoryEnv(t)
	pa, _ := LoadOrCreate(cwd)
	_ = pa.Add(Entry{Name: "x", Tagline: "x"})

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `[]`, // reasoner says "nothing qualifies"
		}}}},
	}
	done := make(chan struct{})
	h := &Hook{Project: pa, Dreamer: &Dreamer{Client: fake}, autoDreamDone: done}
	h.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not complete")
	}

	soul, _ := LoadSoul()
	if soul.Pending != "" {
		t.Errorf("empty dream output should NOT touch soul.Pending; got %q", soul.Pending)
	}
}

// contains is a tiny shim so the test file's import list stays tight.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
