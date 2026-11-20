package sessmeta

// Store tests (W5 c_rqcuaa acceptance ①, sandbox: t.TempDir only):
// round-trip, concurrency (both same-sid last-writer-wins and
// different-sid all-survive), archive idempotency, and the
// BindingStore corruption posture. Run with -race in CI.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openAt(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return OpenAt(filepath.Join(dir, "meta.json"), filepath.Join(dir, "audit.jsonl"))
}

func TestSetTitleRoundTrip(t *testing.T) {
	s := openAt(t)
	if _, err := s.SetTitle("sid-a", "新名字"); err != nil {
		t.Fatal(err)
	}
	all, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	if got := all["sid-a"].Title; got != "新名字" {
		t.Fatalf("title = %q, want 新名字", got)
	}
	// Re-read from disk (fresh store over the same paths) — the state
	// survives the process, which is the whole point of 落库.
	s2 := Store{statePath: s.statePath, auditPath: s.auditPath}
	all2, err := s2.All()
	if err != nil {
		t.Fatal(err)
	}
	if got := all2["sid-a"].Title; got != "新名字" {
		t.Fatalf("re-read title = %q, want 新名字", got)
	}
}

func TestSetTitleEmptyDeletesOverride(t *testing.T) {
	s := openAt(t)
	if _, err := s.SetTitle("sid-a", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTitle("sid-a", ""); err != nil {
		t.Fatal(err)
	}
	all, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["sid-a"]; ok {
		t.Fatalf("entry should be withdrawn once no overrides remain, got %v", all)
	}
	// …but an archived session keeps its entry when the title clears.
	if _, err := s.SetTitle("sid-b", "keep"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetArchived("sid-b", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTitle("sid-b", ""); err != nil {
		t.Fatal(err)
	}
	all, _ = s.All()
	if !all["sid-b"].Archived {
		t.Fatal("archived bit must survive title withdrawal")
	}
}

func TestArchiveIdempotent(t *testing.T) {
	s := openAt(t)
	_, changed, err := s.SetArchived("sid-a", true)
	if err != nil || !changed {
		t.Fatalf("first archive: changed=%v err=%v, want true nil", changed, err)
	}
	_, changed, err = s.SetArchived("sid-a", true)
	if err != nil || changed {
		t.Fatalf("second archive: changed=%v err=%v, want false nil (幂等)", changed, err)
	}
	e, changed, err := s.SetArchived("sid-a", false)
	if err != nil || !changed {
		t.Fatalf("unarchive: changed=%v err=%v, want true nil", changed, err)
	}
	_ = e
	all, _ := s.All()
	if _, ok := all["sid-a"]; ok {
		t.Fatal("unarchive with no title should withdraw the entry")
	}
}

// TestConcurrentWrites: the acceptance ① concurrency leg. Different
// sids must all survive (no torn merge); same-sid renames must leave
// a valid file holding exactly one of the written titles.
func TestConcurrentWrites(t *testing.T) {
	s := openAt(t)
	const n = 32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid := "sid-conc-" + string(rune('a'+i%8))
			if _, err := s.SetTitle(sid, "t"+strings.Repeat("x", i%5+1)); err != nil {
				t.Errorf("SetTitle: %v", err)
			}
			if _, _, err := s.SetArchived(sid, i%2 == 0); err != nil {
				t.Errorf("SetArchived: %v", err)
			}
		}(i)
	}
	wg.Wait()
	all, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 8 {
		t.Fatalf("distinct sids = %d, want 8 (a torn merge would lose some)", len(all))
	}
	// The state file itself must be valid JSON (a concurrent rewrite
	// that interleaved would leave garbage).
	raw, err := os.ReadFile(s.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var check map[string]Entry
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("state file not valid JSON after concurrency: %v", err)
	}
}

// TestCorruptionPosture: garbage state reads as empty and the next
// write heals the file (BindingStore stance).
func TestCorruptionPosture(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(state, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := OpenAt(state, filepath.Join(dir, "audit.jsonl"))
	all, err := s.All()
	if err != nil {
		t.Fatalf("corrupt state must read as empty, got %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("corrupt state must read as empty, got %v", all)
	}
	if _, err := s.SetTitle("sid-a", "healed"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(state)
	if !json.Valid(raw) {
		t.Fatal("write after corruption must leave a valid file")
	}
}

// TestAuditRows: only applied mutations leave rows — rename logs
// old→new, idempotent archive logs nothing.
func TestAuditRows(t *testing.T) {
	dir := t.TempDir()
	s := OpenAt(filepath.Join(dir, "meta.json"), filepath.Join(dir, "audit.jsonl"))
	if _, err := s.SetTitle("sid-a", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTitle("sid-a", "two"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetArchived("sid-a", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetArchived("sid-a", true); err != nil {
		t.Fatal(err) // no-op: no row
	}
	raw, err := os.ReadFile(s.auditPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("audit lines = %d, want 3 (rename, rename, archive — no no-op row):\n%s", len(lines), raw)
	}
	var first, second, third map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first["event"] != "rename" || first["title"] != "one" {
		t.Fatalf("first audit row = %v", first)
	}
	if _, has := first["prev"]; has {
		t.Fatalf("derived-title rename has no prev override: %v", first)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if second["prev"] != "one" || second["title"] != "two" {
		t.Fatalf("second audit row = %v", second)
	}
	if err := json.Unmarshal([]byte(lines[2]), &third); err != nil {
		t.Fatal(err)
	}
	if third["event"] != "archive" {
		t.Fatalf("third audit row = %v", third)
	}
}

// TestAuditDisabled: empty audit path turns logging off silently.
func TestAuditDisabled(t *testing.T) {
	dir := t.TempDir()
	s := OpenAt(filepath.Join(dir, "meta.json"), "")
	if _, err := s.SetTitle("sid-a", "x"); err != nil {
		t.Fatalf("audit-disabled write must still succeed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.jsonl")); !os.IsNotExist(err) {
		t.Fatal("no audit file should appear")
	}
}
