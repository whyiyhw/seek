package hooksconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sampleTOML = `
[[pre_tool]]
name        = "block kubectl delete"
match       = { tool = "bash" }
command     = "grep -v 'kubectl delete'"
timeout_ms  = 1500

[[pre_tool]]
name        = "audit all"
match       = { tool = "*" }
command     = "echo audited"

[[post_tool]]
name        = "lint after edit"
match       = { tool = "edit" }
command     = "make lint"
timeout_ms  = 10000

[[pre_prompt]]
name        = "ping"
command     = "echo ping"

[[session_start]]
name        = "warm"
command     = "true"

[[session_end]]
name        = "report"
command     = "true"
`

func TestDecode_HappyPath(t *testing.T) {
	cfg, err := DecodeBytes([]byte(sampleTOML), SourceUser)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	if len(cfg.PreTool) != 2 {
		t.Errorf("PreTool: want 2 got %d", len(cfg.PreTool))
	}
	if cfg.PreTool[0].Match.Tool != "bash" {
		t.Errorf("first pre_tool match.tool = %q, want bash", cfg.PreTool[0].Match.Tool)
	}
	if !cfg.PreTool[1].MatchTool("anything") {
		t.Errorf("'*' match should accept all tools")
	}
	if cfg.PreTool[0].Source != SourceUser {
		t.Errorf("source not stamped")
	}
	if cfg.PreTool[0].Event != EventPreTool {
		t.Errorf("event not stamped")
	}
	if cfg.PreTool[0].EffectiveTimeoutMs() != 1500 {
		t.Errorf("timeout: want 1500 got %d", cfg.PreTool[0].EffectiveTimeoutMs())
	}
	if cfg.PreTool[1].EffectiveTimeoutMs() != DefaultTimeoutMs {
		t.Errorf("default timeout not applied")
	}
}

func TestMatchTool_ExactName(t *testing.T) {
	h := Hook{Match: Match{Tool: "edit"}}
	if h.MatchTool("bash") {
		t.Errorf("edit-only should not match bash")
	}
	if !h.MatchTool("edit") {
		t.Errorf("edit should match itself")
	}
}

func TestLoad_FileMissingIsEmpty(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"), SourceUser)
	if err != nil {
		t.Fatalf("Load nonexistent: %v", err)
	}
	if !cfg.IsEmpty() {
		t.Errorf("empty Config expected")
	}
}

func TestMerge_ProjectFirst(t *testing.T) {
	user, _ := DecodeBytes([]byte(`[[pre_tool]]
name = "user audit"
command = "echo u"
`), SourceUser)
	project, _ := DecodeBytes([]byte(`[[pre_tool]]
name = "team policy"
command = "echo p"
`), SourceProject)
	merged := Merge(project, user)
	if len(merged.PreTool) != 2 {
		t.Fatalf("merged len: %d", len(merged.PreTool))
	}
	if merged.PreTool[0].Name != "team policy" {
		t.Errorf("project should come first, got %q", merged.PreTool[0].Name)
	}
	if merged.PreTool[1].Name != "user audit" {
		t.Errorf("user should come second, got %q", merged.PreTool[1].Name)
	}
}

func TestValidate_RejectsEmpty(t *testing.T) {
	cfg := Config{PreTool: []Hook{{Name: "", Command: "echo"}}}
	err := Validate(cfg)
	if err == nil {
		t.Fatalf("expected error for empty name")
	}
	if !errors.Is(err, ErrEmptyHook) {
		t.Errorf("expected ErrEmptyHook, got %v", err)
	}
}

func TestStaticCheck_SkipsBadShell(t *testing.T) {
	cfg := Config{
		PreTool: []Hook{
			{Name: "good", Command: "echo ok", Source: SourceUser, Event: EventPreTool},
			{Name: "bad", Command: "echo missing-end-quote '", Source: SourceUser, Event: EventPreTool},
		},
	}
	fake := func(cmd string) error {
		if strings.Contains(cmd, "missing-end-quote") {
			return errors.New("syntax error: unterminated quoted string")
		}
		return nil
	}
	warnings := StaticCheck(&cfg, fake)
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %d (%v)", len(warnings), warnings)
	}
	if cfg.PreTool[0].SkipReason != "" {
		t.Errorf("good hook flagged as skip: %q", cfg.PreTool[0].SkipReason)
	}
	if cfg.PreTool[1].SkipReason == "" {
		t.Errorf("bad hook missing SkipReason")
	}
}

func TestStaticCheck_DefaultsToDefaultChecker(t *testing.T) {
	// nil checker shouldn't panic — it picks DefaultSyntaxChecker.
	// Use a trivially-good command so we don't depend on bash being
	// installed; if bash is missing the checker returns an err but
	// won't panic, satisfying the test name's intent.
	cfg := Config{PreTool: []Hook{{Name: "x", Command: "true"}}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	_ = StaticCheck(&cfg, nil)
}

func TestSha256Hex_Stable(t *testing.T) {
	a := Sha256Hex([]byte("hello"))
	b := Sha256Hex([]byte("hello"))
	if a != b {
		t.Errorf("sha not stable")
	}
	if len(a) != 64 {
		t.Errorf("hex len: %d", len(a))
	}
}

func TestSha256File_MissingReturnsENOENT(t *testing.T) {
	_, err := Sha256File(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("want ENOENT, got %v", err)
	}
}

// ---- TrustStore ----

func TestTrustStore_FreshIsEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trust.json")
	s, err := NewTrustStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.IsTrusted("/x", "abc") {
		t.Errorf("fresh store should not trust anything")
	}
}

func TestTrustStore_ApproveThenIsTrusted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trust.json")
	s, _ := NewTrustStore(p)
	if err := s.Approve("/x", "abc", "2026-05-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if !s.IsTrusted("/x", "abc") {
		t.Errorf("should be trusted after Approve")
	}
	if s.IsTrusted("/x", "different-sha") {
		t.Errorf("different sha should re-prompt (PRD §3.5 sha change re-asks)")
	}
}

func TestTrustStore_RoundTripFromDisk(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trust.json")
	s, _ := NewTrustStore(p)
	_ = s.Approve("/x", "abc", "2026-05-27T00:00:00Z")
	// Re-open from disk
	s2, err := NewTrustStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.IsTrusted("/x", "abc") {
		t.Errorf("trust did not survive round trip")
	}
}

func TestTrustStore_Reset(t *testing.T) {
	p := filepath.Join(t.TempDir(), "trust.json")
	s, _ := NewTrustStore(p)
	_ = s.Approve("/x", "abc", "2026-05-27T00:00:00Z")
	_ = s.Reset("/x")
	if s.IsTrusted("/x", "abc") {
		t.Errorf("Reset should clear trust")
	}
}

// ---- AuditLog ----

func TestAuditLog_Append(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	a, err := NewAuditLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Append(AuditEntry{Event: EventPreTool, Hook: "h", Tool: "bash", ExitCode: 1, Denied: true, DurationMs: 42}); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadAuditLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Hook != "h" || !entries[0].Denied {
		t.Errorf("round trip failed: %+v", entries[0])
	}
}

func TestAuditLog_ConcurrentAppendNoTear(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	a, _ := NewAuditLog(p)
	defer a.Close()

	const N = 100
	const Workers = 8
	var wg sync.WaitGroup
	var ts atomic.Int64

	for w := range Workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range N {
				ts.Add(1)
				_ = a.Append(AuditEntry{
					Event: EventPostTool, Hook: fmt.Sprintf("w%d-h%d", id, i), Tool: "bash", DurationMs: int64(i),
				})
			}
		}(w)
	}
	wg.Wait()

	entries, err := ReadAuditLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(entries), N*Workers; got != want {
		t.Errorf("entries: got %d want %d (lines may have torn)", got, want)
	}
}

func TestAverageDurationByHook(t *testing.T) {
	entries := []AuditEntry{
		{Hook: "a", DurationMs: 100},
		{Hook: "a", DurationMs: 200},
		{Hook: "b", DurationMs: 50},
	}
	avg := AverageDurationByHook(entries, 0)
	if avg["a"] != 150 {
		t.Errorf("a avg: got %v want 150", avg["a"])
	}
	if avg["b"] != 50 {
		t.Errorf("b avg: got %v want 50", avg["b"])
	}
}

// ---- Gate ----

type fakePrompt struct {
	answer  bool
	called  int32
	lastReq TrustRequest
}

func (f *fakePrompt) AskTrustHooks(req TrustRequest) bool {
	atomic.AddInt32(&f.called, 1)
	f.lastReq = req
	return f.answer
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGate_NoFiles_NoHooks(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewTrustStore(filepath.Join(dir, "trust.json"))
	cfg, warnings := Gate(
		filepath.Join(dir, "user.toml"),
		filepath.Join(dir, "project.toml"),
		"/abs/proj",
		store,
		nil,
		func(string) error { return nil },
	)
	if !cfg.IsEmpty() {
		t.Errorf("expected empty cfg")
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestGate_UserOnly(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.toml")
	writeFile(t, userPath, `[[pre_tool]]
name = "user-only"
command = "echo u"
`)
	store, _ := NewTrustStore(filepath.Join(dir, "trust.json"))
	cfg, warnings := Gate(userPath, filepath.Join(dir, "noproj.toml"), "/abs/proj", store, nil, func(string) error { return nil })
	if len(cfg.PreTool) != 1 {
		t.Fatalf("want 1, got %d", len(cfg.PreTool))
	}
	if cfg.PreTool[0].Source != SourceUser {
		t.Errorf("source wrong: %s", cfg.PreTool[0].Source)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
}

func TestGate_ProjectAsksTrustOnFirstVisit(t *testing.T) {
	dir := t.TempDir()
	projPath := filepath.Join(dir, "project.toml")
	writeFile(t, projPath, `[[pre_tool]]
name = "proj"
command = "echo p"
`)
	store, _ := NewTrustStore(filepath.Join(dir, "trust.json"))
	prompt := &fakePrompt{answer: true}
	cfg, warnings := Gate(filepath.Join(dir, "user.toml"), projPath, "/abs/proj", store, prompt, func(string) error { return nil })
	if atomic.LoadInt32(&prompt.called) != 1 {
		t.Errorf("expected 1 prompt, got %d", prompt.called)
	}
	if len(cfg.PreTool) != 1 {
		t.Fatalf("expected 1 hook after approval, got %d (warnings: %v)", len(cfg.PreTool), warnings)
	}
	// Same SHA -> should NOT re-prompt
	prompt2 := &fakePrompt{answer: false}
	_, _ = Gate(filepath.Join(dir, "user.toml"), projPath, "/abs/proj", store, prompt2, func(string) error { return nil })
	if atomic.LoadInt32(&prompt2.called) != 0 {
		t.Errorf("trusted second visit should not re-prompt; called=%d", prompt2.called)
	}
}

func TestGate_ProjectShaChangeReprompts(t *testing.T) {
	dir := t.TempDir()
	projPath := filepath.Join(dir, "project.toml")
	writeFile(t, projPath, `[[pre_tool]]
name = "v1"
command = "true"
`)
	store, _ := NewTrustStore(filepath.Join(dir, "trust.json"))
	prompt := &fakePrompt{answer: true}
	_, _ = Gate("", projPath, "/abs", store, prompt, func(string) error { return nil })
	if prompt.called != 1 {
		t.Fatalf("first visit: %d", prompt.called)
	}

	// Modify file -> sha changes -> re-prompt.
	writeFile(t, projPath, `[[pre_tool]]
name = "v2"
command = "true"
`)
	prompt.called = 0
	_, _ = Gate("", projPath, "/abs", store, prompt, func(string) error { return nil })
	if prompt.called != 1 {
		t.Errorf("changed file should re-prompt, got %d", prompt.called)
	}
}

func TestGate_ProjectRefused_HooksDisabled(t *testing.T) {
	dir := t.TempDir()
	projPath := filepath.Join(dir, "project.toml")
	writeFile(t, projPath, `[[pre_tool]]
name = "rejected"
command = "true"
`)
	store, _ := NewTrustStore(filepath.Join(dir, "trust.json"))
	prompt := &fakePrompt{answer: false}
	cfg, warnings := Gate("", projPath, "/abs", store, prompt, func(string) error { return nil })
	if !cfg.IsEmpty() {
		t.Errorf("refused project should yield empty cfg")
	}
	if len(warnings) == 0 {
		t.Errorf("expected a warning explaining the refusal")
	}
}

func TestGate_NoPromptAvailable_ProjectDropped(t *testing.T) {
	dir := t.TempDir()
	projPath := filepath.Join(dir, "project.toml")
	writeFile(t, projPath, `[[pre_tool]]
name = "anon"
command = "true"
`)
	store, _ := NewTrustStore(filepath.Join(dir, "trust.json"))
	cfg, warnings := Gate("", projPath, "/abs", store, nil, func(string) error { return nil })
	if !cfg.IsEmpty() {
		t.Errorf("no prompt should drop project hooks")
	}
	if len(warnings) == 0 {
		t.Errorf("expected warning about missing prompt")
	}
}

func TestGate_NoBashExecBeforeTrust(t *testing.T) {
	// The PRD says "trust 询问之前**不会**调用任何 bash -c". The Gate path is
	// supposed to never call a syntax checker before trust completes.
	// We verify this by passing a checker that records calls and
	// asserting the recorder is empty when trust is refused.
	dir := t.TempDir()
	projPath := filepath.Join(dir, "project.toml")
	writeFile(t, projPath, `[[pre_tool]]
name = "x"
command = "true"
`)
	store, _ := NewTrustStore(filepath.Join(dir, "trust.json"))
	called := atomic.Int32{}
	checker := func(cmd string) error {
		called.Add(1)
		return nil
	}
	prompt := &fakePrompt{answer: false}
	_, _ = Gate("", projPath, "/abs", store, prompt, checker)
	if called.Load() != 0 {
		t.Errorf("syntax checker (bash -n) called %d times before/after trust refusal; want 0", called.Load())
	}
}

// ---- Summarize ----

func TestSummarize_OrderAndSkipReason(t *testing.T) {
	cfg, _ := DecodeBytes([]byte(sampleTOML), SourceUser)
	cfg.PreTool[1].SkipReason = "broken"
	sum := Summarize(cfg)
	if len(sum) < 4 {
		t.Fatalf("too few summary rows: %d", len(sum))
	}
	if sum[0].Event != EventPreTool {
		t.Errorf("first event: %s", sum[0].Event)
	}
	// pre_tool comes first, audit-all is index 1 with SkipReason set
	if sum[1].SkipReason != "broken" {
		t.Errorf("SkipReason not propagated: %q", sum[1].SkipReason)
	}
}

func TestSortByName(t *testing.T) {
	s := []HookSummary{{Name: "b"}, {Name: "a"}, {Name: "c"}}
	SortByName(s)
	if s[0].Name != "a" || s[1].Name != "b" || s[2].Name != "c" {
		t.Errorf("sort failed: %+v", s)
	}
}

// ---- time round trip sanity ----

func TestAuditLog_TimestampUsesClock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	a, _ := NewAuditLog(p)
	defer a.Close()
	fixed := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return fixed }
	_ = a.Append(AuditEntry{Event: EventPreTool, Hook: "h"})
	entries, _ := ReadAuditLog(p)
	if entries[0].TS != "2026-05-27T12:00:00Z" {
		t.Errorf("clock not honored: %q", entries[0].TS)
	}
}

// ---- P3 observer events (feature-orchestration §6 D15) ----

const sampleP3TOML = `
[[subagent_start]]
name = "bg-announce"
command = "echo started"

[[subagent_stop]]
name = "bg-lint"
command = "make lint"

[[stop]]
name = "chime"
command = "afplay ding.aiff"

[[pre_compact]]
name = "snapshot"
command = "cp transcript.jsonl backup.jsonl"

[[notification]]
name = "mirror"
command = "echo notified"
`

func TestDecode_P3EventsStamped(t *testing.T) {
	cfg, err := DecodeBytes([]byte(sampleP3TOML), SourceProject)
	if err != nil {
		t.Fatalf("DecodeBytes: %v", err)
	}
	for _, tc := range []struct {
		event string
		hooks []Hook
	}{
		{EventSubagentStart, cfg.SubagentStart},
		{EventSubagentStop, cfg.SubagentStop},
		{EventStop, cfg.Stop},
		{EventPreCompact, cfg.PreCompact},
		{EventNotification, cfg.Notification},
	} {
		if len(tc.hooks) != 1 {
			t.Errorf("%s: want 1 hook got %d", tc.event, len(tc.hooks))
			continue
		}
		if tc.hooks[0].Event != tc.event {
			t.Errorf("%s: event stamp = %q", tc.event, tc.hooks[0].Event)
		}
		if tc.hooks[0].Source != SourceProject {
			t.Errorf("%s: source stamp = %q", tc.event, tc.hooks[0].Source)
		}
	}
	if cfg.IsEmpty() {
		t.Errorf("P3 hooks should make IsEmpty false")
	}
}

func TestMerge_P3EventsProjectFirst(t *testing.T) {
	user, _ := DecodeBytes([]byte(`
[[stop]]
name = "user stop"
command = "echo u"
`), SourceUser)
	project, _ := DecodeBytes([]byte(`
[[stop]]
name = "team stop"
command = "echo p"

[[notification]]
name = "team notify"
command = "echo n"
`), SourceProject)
	merged := Merge(project, user)
	if len(merged.Stop) != 2 || merged.Stop[0].Name != "team stop" || merged.Stop[1].Name != "user stop" {
		t.Errorf("merged Stop order: %+v", merged.Stop)
	}
	if len(merged.Notification) != 1 || merged.Notification[0].Name != "team notify" {
		t.Errorf("merged Notification: %+v", merged.Notification)
	}
	all := merged.All()
	if len(all) != 3 {
		t.Errorf("All(): want 3 got %d", len(all))
	}
}

func TestSummarize_IncludesP3EventsInOrder(t *testing.T) {
	cfg, _ := DecodeBytes([]byte(sampleP3TOML), SourceUser)
	sum := Summarize(cfg)
	if len(sum) != 5 {
		t.Fatalf("Summarize: want 5 got %d", len(sum))
	}
	wantOrder := []string{
		EventSubagentStart, EventSubagentStop, EventStop, EventPreCompact, EventNotification,
	}
	for i, ev := range wantOrder {
		if sum[i].Event != ev {
			t.Errorf("Summarize[%d].Event = %q, want %q", i, sum[i].Event, ev)
		}
	}
}

// TestAuditEntry_SubagentSIDWireShape: subagent_sid is omitempty — a
// row without it marshals exactly like the pre-M-O3.1 wire shape
// (web W4.6's hooks/audit/tail re-marshals these entries into RPC
// results; old rows must not grow the key), and a row with it keeps
// the documented spelling.
func TestAuditEntry_SubagentSIDWireShape(t *testing.T) {
	old, err := json.Marshal(AuditEntry{TS: "t", Event: EventPreTool, Hook: "h", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(old), "subagent_sid") {
		t.Errorf("old-shape row grew subagent_sid: %s", old)
	}
	neu, err := json.Marshal(AuditEntry{Event: EventSubagentStop, Hook: "h", SessionID: "s", SubagentSID: "sub-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(neu), `"subagent_sid":"sub-1"`) {
		t.Errorf("subagent row missing subagent_sid: %s", neu)
	}
	// And the old JSON round-trips through the new struct.
	var back AuditEntry
	if err := json.Unmarshal(old, &back); err != nil {
		t.Fatalf("old line no longer parses: %v", err)
	}
	if back.SubagentSID != "" || back.Event != EventPreTool {
		t.Errorf("round-trip drifted: %+v", back)
	}
}
