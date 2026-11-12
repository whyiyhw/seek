package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenBindingStore_ResolvesUnderServeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEEK_HOME", dir)
	s, err := OpenBindingStore()
	if err != nil {
		t.Fatalf("OpenBindingStore: %v", err)
	}
	want := filepath.Join(dir, "serve", "bindings.json")
	if s.Path() != want {
		t.Errorf("Path() = %q, want %q", s.Path(), want)
	}
}

func TestBindingStore_MissingFileIsEmpty(t *testing.T) {
	s := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))

	got, err := s.List()
	if err != nil {
		t.Fatalf("List on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List on missing file = %+v, want empty", got)
	}
	if _, err := s.Get(NewConvID("feishu", "c1")); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("Get on missing file: err = %v, want ErrBindingNotFound", err)
	}
}

func TestBindingStore_RoundTrip(t *testing.T) {
	s := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))

	put := []Binding{
		{ConvID: NewConvID("feishu", "oc_b"), ProjectPath: "/tmp/proj-b", SessionID: "sid-b"},
		{ConvID: NewConvID("feishu", "oc_a"), ProjectPath: "/tmp/proj-a"}, // session still empty
		{ConvID: NewConvID("telegram", "oc_a"), ProjectPath: "/tmp/proj-t", Yolo: true},
	}
	for _, b := range put {
		if err := s.Put(b); err != nil {
			t.Fatalf("Put %+v: %v", b, err)
		}
	}

	// Fresh instance over the same path: persistence must survive a
	// serve-process restart, not just the in-process handle.
	reopened := OpenBindingStoreAt(s.Path())
	got, err := reopened.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != len(put) {
		t.Fatalf("reloaded %d bindings, want %d: %+v", len(got), len(put), got)
	}
	// List is sorted by ConvID.
	wantOrder := []ConvID{"feishu:oc_a", "feishu:oc_b", "telegram:oc_a"}
	for i, b := range got {
		if b.ConvID != wantOrder[i] {
			t.Errorf("List[%d].ConvID = %q, want %q", i, b.ConvID, wantOrder[i])
		}
	}
	// Field-level round-trip (UpdatedAt excluded — Put stamps it).
	a, err := reopened.Get(NewConvID("feishu", "oc_a"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a.ProjectPath != "/tmp/proj-a" || a.SessionID != "" || a.Yolo {
		t.Errorf("feishu:oc_a round-trip = %+v", a)
	}
	if a.UpdatedAt.IsZero() {
		t.Errorf("Put must stamp UpdatedAt, got zero")
	}
	tg, err := reopened.Get(NewConvID("telegram", "oc_a"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !tg.Yolo || tg.SessionID != "" || tg.ProjectPath != "/tmp/proj-t" {
		t.Errorf("telegram:oc_a round-trip = %+v", tg)
	}

	// The on-disk file is a valid JSON array.
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var check []Binding
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("on-disk file is not valid JSON: %v\n%s", err, raw)
	}
	if len(check) != 3 {
		t.Errorf("on-disk entries = %d, want 3", len(check))
	}
}

func TestBindingStore_PutUpsertsAndStamps(t *testing.T) {
	s := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))
	conv := NewConvID("feishu", "oc_1")

	if err := s.Put(Binding{ConvID: conv, ProjectPath: "/p1", SessionID: "s1"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// /switch shape: same conv, new project, session cleared.
	if err := s.Put(Binding{ConvID: conv, ProjectPath: "/p2"}); err != nil {
		t.Fatalf("Put #2: %v", err)
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ProjectPath != "/p2" || got[0].SessionID != "" {
		t.Fatalf("upsert = %+v, want single binding /p2 with empty session", got)
	}

	// UpdatedAt advances on mutation.
	first := got[0].UpdatedAt
	time.Sleep(2 * time.Millisecond) // ensure clock tick
	if err := s.Put(Binding{ConvID: conv, ProjectPath: "/p3"}); err != nil {
		t.Fatalf("Put #3: %v", err)
	}
	second, _ := s.Get(conv)
	if !second.UpdatedAt.After(first) {
		t.Errorf("UpdatedAt did not advance: %v → %v", first, second.UpdatedAt)
	}
}

func TestBindingStore_PutValidation(t *testing.T) {
	s := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))
	tests := []struct {
		name string
		b    Binding
		want string
	}{
		{"empty conv", Binding{ProjectPath: "/p"}, "conv_id"},
		{"no separator", Binding{ConvID: "oc_1", ProjectPath: "/p"}, "conv_id"},
		{"empty native", Binding{ConvID: "feishu:", ProjectPath: "/p"}, "conv_id"},
		{"empty project", Binding{ConvID: NewConvID("feishu", "oc_1")}, "project path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Put(tt.b)
			if err == nil {
				t.Fatal("Put accepted invalid binding")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want mention of %q", err, tt.want)
			}
			// Nothing was written.
			got, lerr := s.List()
			if lerr != nil || len(got) != 0 {
				t.Errorf("invalid Put leaked state: %+v (%v)", got, lerr)
			}
		})
	}
}

func TestBindingStore_Delete(t *testing.T) {
	s := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))
	conv := NewConvID("feishu", "oc_1")
	if err := s.Put(Binding{ConvID: conv, ProjectPath: "/p"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(conv); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(conv); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrBindingNotFound", err)
	}
	// Deleting an absent binding surfaces ErrBindingNotFound.
	if err := s.Delete(conv); !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("Delete absent: err = %v, want ErrBindingNotFound", err)
	}
}

// TestBindingStore_CorruptFileRebuilds covers the "损坏重建" bar: an
// unparseable file must not brick the plane. The store comes back
// empty and the next Put rewrites a valid file (self-heal on write).
func TestBindingStore_CorruptFileRebuilds(t *testing.T) {
	for name, garbage := range map[string]string{
		"binary garbage": "\x00\x01not json at all{{{",
		"truncated json": ` [{"conv_id":"feishu:oc_1","project_abs_path":"/p"`,
		"wrong shape":    `{"conv_id":"feishu:oc_1"}`,
		"null":           `null`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bindings.json")
			if err := os.WriteFile(path, []byte(garbage), 0o600); err != nil {
				t.Fatalf("seed corrupt file: %v", err)
			}
			s := OpenBindingStoreAt(path)

			got, err := s.List()
			if err != nil {
				t.Fatalf("List over corrupt file: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("corrupt file yielded bindings: %+v", got)
			}

			// Self-heal: next mutation leaves a valid file behind.
			if err := s.Put(Binding{ConvID: NewConvID("feishu", "oc_2"), ProjectPath: "/p2"}); err != nil {
				t.Fatalf("Put after corruption: %v", err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read healed file: %v", err)
			}
			var check []Binding
			if err := json.Unmarshal(raw, &check); err != nil {
				t.Fatalf("file still corrupt after Put: %v\n%s", err, raw)
			}
		})
	}
}

// TestBindingStore_EmptyFileTolerated: a zero-byte file (manual
// truncation — the atomic rename can't produce one) reads as a valid
// empty store, same as ENOENT.
func TestBindingStore_EmptyFileTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}
	s := OpenBindingStoreAt(path)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List over empty file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty file yielded bindings: %+v", got)
	}
}

// TestBindingStore_MalformedEntrySkipped: entry-level corruption only
// costs the bad entry — the same tolerance as routines' jobs.jsonl
// loader, adapted to a single-JSON-array file.
func TestBindingStore_MalformedEntrySkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	good := Binding{
		ConvID:      NewConvID("feishu", "oc_good"),
		ProjectPath: "/proj/good",
		SessionID:   "sid-1",
		UpdatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	doc := fmt.Sprintf("[%s,%s,%s,%s]",
		mustJSON(t, good),
		`{"conv_id":"feishu:oc_badtype","project_abs_path":123}`, // bad field type
		`{"conv_id":"no-separator","project_abs_path":"/p"}`,     // invalid conv_id
		`{"project_abs_path":"/p"}`,                              // missing conv_id
	)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	s := OpenBindingStoreAt(path)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %+v, want exactly the good entry", got)
	}
	if got[0].ConvID != good.ConvID || got[0].ProjectPath != good.ProjectPath || got[0].SessionID != good.SessionID {
		t.Errorf("good entry round-trip = %+v", got[0])
	}
	if !got[0].UpdatedAt.Equal(good.UpdatedAt) {
		t.Errorf("UpdatedAt drifted: %v vs %v", got[0].UpdatedAt, good.UpdatedAt)
	}
}

// TestBindingStore_DuplicateLastWins: duplicate conv_id entries
// resolve to the last one — matching Put's upsert semantics, so a
// hand-merged file behaves like the sequence of writes it encodes.
func TestBindingStore_DuplicateLastWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	doc := `[
		{"conv_id":"feishu:oc_1","project_abs_path":"/old","updated_at":"2026-01-01T00:00:00Z"},
		{"conv_id":"feishu:oc_1","project_abs_path":"/new","updated_at":"2026-01-02T00:00:00Z"}
	]`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	s := OpenBindingStoreAt(path)
	got, err := s.Get(NewConvID("feishu", "oc_1"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ProjectPath != "/new" {
		t.Errorf("duplicate conv_id resolved to %q, want last (/new)", got.ProjectPath)
	}
}

// TestBindingStore_ConcurrentPuts races Put/Delete/Get from many
// goroutines. -race CI mode is the point; the assertion is that the
// final file is valid JSON and consistent with a sequential replay of
// the surviving bindings.
func TestBindingStore_ConcurrentPuts(t *testing.T) {
	s := OpenBindingStoreAt(filepath.Join(t.TempDir(), "bindings.json"))
	const workers, perWorker = 8, 25

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				conv := NewConvID("feishu", fmt.Sprintf("c-%d-%d", w, i))
				if err := s.Put(Binding{ConvID: conv, ProjectPath: fmt.Sprintf("/p/%d/%d", w, i)}); err != nil {
					t.Errorf("Put %s: %v", conv, err)
					return
				}
				if _, err := s.Get(conv); err != nil {
					t.Errorf("Get %s: %v", conv, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != workers*perWorker {
		t.Errorf("survived %d bindings, want %d", len(got), workers*perWorker)
	}
}

// TestBindingStore_FilePermissionsAndNoTmp: the bindings file is
// created 0600 (same trust level as the sibling env credential file)
// and a successful write leaves no .tmp litter behind.
func TestBindingStore_FilePermissionsAndNoTmp(t *testing.T) {
	dir := t.TempDir()
	s := OpenBindingStoreAt(filepath.Join(dir, "bindings.json"))
	if err := s.Put(Binding{ConvID: NewConvID("feishu", "oc_1"), ProjectPath: "/p"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fi, err := os.Stat(s.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("bindings.json mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file after successful write: %s", e.Name())
		}
	}
}

// TestBindingStore_ServeDirCreatedLazily: Put into a not-yet-existing
// serve directory works (MkdirAll at write time, 0700).
func TestBindingStore_ServeDirCreatedLazily(t *testing.T) {
	root := t.TempDir()
	servePath := filepath.Join(root, "serve", "nested", "bindings.json")
	s := OpenBindingStoreAt(servePath)
	if err := s.Put(Binding{ConvID: NewConvID("feishu", "oc_1"), ProjectPath: "/p"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fi, err := os.Stat(filepath.Dir(servePath))
	if err != nil {
		t.Fatalf("stat serve dir: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("serve dir mode = %o, want 700", perm)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// Compile-time guards for the nil-store paths (defensive returns kept
// honest): a nil store must error, not panic the plane.
func TestBindingStore_NilStoreErrors(t *testing.T) {
	var s *BindingStore
	if _, err := s.List(); err == nil {
		t.Error("nil List should error")
	}
	if _, err := s.Get(NewConvID("feishu", "c")); err == nil {
		t.Error("nil Get should error")
	}
	if err := s.Put(Binding{ConvID: NewConvID("feishu", "c"), ProjectPath: "/p"}); err == nil {
		t.Error("nil Put should error")
	}
	if err := s.Delete(NewConvID("feishu", "c")); err == nil {
		t.Error("nil Delete should error")
	}
}
