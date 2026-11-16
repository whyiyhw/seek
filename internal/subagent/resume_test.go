package subagent

// Resume-backfill tests (feature-orchestration PRD §4.1 D3, M-O1.3):
// hand-crafted subagents.jsonl + child transcripts folded by
// recoverNotices — selection (completed-unnotified only, this parent,
// background only), summary-vs-status-note shaping, terminal-time
// FIFO, idempotency (repeated resume never re-enqueues after the
// notified marker lands), and the Notifier's notified write surface.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
)

// writeIndexLines writes raw JSONL event lines to the project index,
// creating parents as needed — the hand-crafted-fixture counterpart
// of appendEvent (tests must not go through the writer under test).
func writeIndexLines(t *testing.T, project string, lines ...string) {
	t.Helper()
	indexPath, err := paths.SubagentsIndex(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const trHeader = `{"schema_version":2,"id":"h","created_at":"2026-01-01T00:00:00Z"}`

// wantNotice is the per-case expectation: which sub_sid produces a
// notice, which prefix it must carry, and extra fragments the body
// must contain (checked with Contains so the full wire format isn't
// re-asserted line-by-line here — the formatters have their own
// tests).
type wantNotice struct {
	subSid     string
	prefix     string
	contains   []string
	notContain []string
}

func TestRecoverNotices(t *testing.T) {
	// One shared project per case (each t.Run gets fresh SEEK_HOME via
	// withHome inside the loop — the helpers resolve paths through it).
	cases := []struct {
		name        string
		indexLines  []string
		transcripts map[string][]string // subSid -> transcript lines; absent = no file
		parentSid   string
		want        []wantNotice
	}{
		{
			name: "completed background un-notified gets summary notice",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-done","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"deep audit","background":true}`,
				`{"event":"completed","sub_sid":"sub-done","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":700,"completion":90,"cache_hit":600}}`,
			},
			transcripts: map[string][]string{
				"sub-done": {trHeader,
					`{"role":"user","content":"audit"}`,
					`{"role":"assistant","content":"three stale deps found in pkg/deepseek"}`,
				},
			},
			parentSid: "p1",
			want: []wantNotice{{
				subSid:     "sub-done",
				prefix:     "[agent: background done sub-sid=sub-done]",
				contains:   []string{"three stale deps found in pkg/deepseek", "tokens: 790", "delivered on session resume"},
				notContain: []string{"turns:"},
			}},
		},
		{
			name: "notified completed row is skipped (idempotent selection)",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-seen","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"d","background":true}`,
				`{"event":"completed","sub_sid":"sub-seen","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":10,"completion":2,"cache_hit":0}}`,
				`{"event":"notified","sub_sid":"sub-seen","ts":"2026-01-02T10:06:00Z","parent_turn":8}`,
			},
			transcripts: map[string][]string{
				"sub-seen": {trHeader, `{"role":"assistant","content":"already delivered once"}`},
			},
			parentSid: "p1",
			want:      nil,
		},
		{
			name: "sync (non-background) completed row is skipped — tool result already carried it",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-sync","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"d"}`,
				`{"event":"completed","sub_sid":"sub-sync","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":10,"completion":2,"cache_hit":0}}`,
			},
			transcripts: map[string][]string{
				"sub-sync": {trHeader, `{"role":"assistant","content":"sync result"}`},
			},
			parentSid: "p1",
			want:      nil,
		},
		{
			name: "other parent session's rows are skipped",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-foreign","parent_sid":"p2","ts":"2026-01-02T10:00:00Z","type":"explore","description":"d","background":true}`,
				`{"event":"completed","sub_sid":"sub-foreign","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":10,"completion":2,"cache_hit":0}}`,
			},
			transcripts: map[string][]string{
				"sub-foreign": {trHeader, `{"role":"assistant","content":"not mine"}`},
			},
			parentSid: "p1",
			want:      nil,
		},
		{
			name: "failed background gets one-line status note, no summary",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-fail","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"general-purpose","description":"doomed task","background":true}`,
				`{"event":"failed","sub_sid":"sub-fail","ts":"2026-01-02T10:03:00Z","reason":"timeout"}`,
			},
			parentSid: "p1",
			want: []wantNotice{{
				subSid:   "sub-fail",
				prefix:   "[agent: background failed reason=timeout]",
				contains: []string{"found on resume"},
			}},
		},
		{
			name: "orphaned background gets lost status note (记录在、transcript 失联)",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-orphan","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"crash residue","background":true}`,
				`{"event":"orphaned","sub_sid":"sub-orphan","ts":"2026-01-02T10:04:00Z"}`,
			},
			parentSid: "p1",
			want: []wantNotice{{
				subSid:   "sub-orphan",
				prefix:   "[agent: background lost sub-sid=sub-orphan] status=orphaned",
				contains: []string{"crash residue", "owning process died"},
			}},
		},
		{
			name: "completed background with missing transcript degrades to lost note",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-gone","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"vanished","background":true}`,
				`{"event":"completed","sub_sid":"sub-gone","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":10,"completion":2,"cache_hit":0}}`,
			},
			parentSid: "p1",
			want: []wantNotice{{
				subSid:   "sub-gone",
				prefix:   "[agent: background lost sub-sid=sub-gone] status=completed",
				contains: []string{"transcript missing"},
			}},
		},
		{
			name: "completed background with header-only transcript degrades to lost note",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-empty","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"empty","background":true}`,
				`{"event":"completed","sub_sid":"sub-empty","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":10,"completion":2,"cache_hit":0}}`,
			},
			transcripts: map[string][]string{
				"sub-empty": {trHeader},
			},
			parentSid: "p1",
			want: []wantNotice{{
				subSid:   "sub-empty",
				prefix:   "[agent: background lost sub-sid=sub-empty] status=completed",
				contains: []string{"transcript missing"},
			}},
		},
		{
			name: "active background row is skipped (not terminal)",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-live","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"running","background":true}`,
			},
			parentSid: "p1",
			want:      nil,
		},
		{
			name: "killed and promoted background rows are skipped (user-visible actions, no delivery debt)",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-killed","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"d","background":true}`,
				`{"event":"killed","sub_sid":"sub-killed","ts":"2026-01-02T10:02:00Z"}`,
				`{"event":"started","sub_sid":"sub-promo","parent_sid":"p1","ts":"2026-01-02T10:03:00Z","type":"explore","description":"d","background":true}`,
				`{"event":"completed","sub_sid":"sub-promo","ts":"2026-01-02T10:04:00Z","tokens":{"prompt":1,"completion":1,"cache_hit":0}}`,
				`{"event":"promoted","sub_sid":"sub-promo","ts":"2026-01-02T10:05:00Z"}`,
			},
			parentSid: "p1",
			want:      nil,
		},
		{
			name: "continuation lineage shows in the resumed notice footer",
			indexLines: []string{
				`{"event":"started","sub_sid":"sub-old","parent_sid":"p1","ts":"2026-01-02T09:00:00Z","type":"explore","description":"first run","background":true}`,
				`{"event":"completed","sub_sid":"sub-old","ts":"2026-01-02T09:10:00Z","tokens":{"prompt":100,"completion":10,"cache_hit":0}}`,
				`{"event":"started","sub_sid":"sub-new","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"follow-up","background":true,"resumed_from":"sub-old"}`,
				`{"event":"completed","sub_sid":"sub-new","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":200,"completion":20,"cache_hit":0}}`,
				`{"event":"notified","sub_sid":"sub-old","ts":"2026-01-02T09:11:00Z"}`,
			},
			transcripts: map[string][]string{
				"sub-old": {trHeader, `{"role":"assistant","content":"old findings"}`},
				"sub-new": {trHeader, `{"role":"assistant","content":"follow-up findings"}`},
			},
			parentSid: "p1",
			want: []wantNotice{{
				subSid:   "sub-new",
				prefix:   "[agent: background done sub-sid=sub-new]",
				contains: []string{"follow-up findings", "resumed-from: old", "tokens: 220"},
			}},
		},
		{
			name: "multiple pending rows come back terminal-time FIFO",
			indexLines: []string{
				// started newest-first on disk (fold order) with terminal
				// times interleaved — FIFO must be by EndedAt, not file order.
				`{"event":"started","sub_sid":"sub-c","parent_sid":"p1","ts":"2026-01-02T10:20:00Z","type":"explore","description":"third","background":true}`,
				`{"event":"completed","sub_sid":"sub-c","ts":"2026-01-02T10:30:00Z","tokens":{"prompt":3,"completion":1,"cache_hit":0}}`,
				`{"event":"started","sub_sid":"sub-a","parent_sid":"p1","ts":"2026-01-02T10:00:00Z","type":"explore","description":"first","background":true}`,
				`{"event":"completed","sub_sid":"sub-a","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":1,"completion":1,"cache_hit":0}}`,
				`{"event":"started","sub_sid":"sub-b","parent_sid":"p1","ts":"2026-01-02T10:10:00Z","type":"explore","description":"second","background":true}`,
				`{"event":"failed","sub_sid":"sub-b","ts":"2026-01-02T10:15:00Z","reason":"spawn_error"}`,
			},
			transcripts: map[string][]string{
				"sub-a": {trHeader, `{"role":"assistant","content":"A findings"}`},
				"sub-c": {trHeader, `{"role":"assistant","content":"C findings"}`},
			},
			parentSid: "p1",
			want: []wantNotice{
				{subSid: "sub-a", prefix: "[agent: background done sub-sid=sub-a]"},
				{subSid: "sub-b", prefix: "[agent: background failed reason=spawn_error]"},
				{subSid: "sub-c", prefix: "[agent: background done sub-sid=sub-c]"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHome(t)
			project := t.TempDir()
			writeIndexLines(t, project, tc.indexLines...)
			for sid, lines := range tc.transcripts {
				writeTranscript(t, project, tc.parentSid, sid, lines...)
			}
			got, err := recoverNotices(project, tc.parentSid)
			if err != nil {
				t.Fatalf("recoverNotices: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("notices = %d, want %d:\n%s", len(got), len(tc.want), dumpNotices(got))
			}
			for i, w := range tc.want {
				if got[i].SubSid != w.subSid {
					t.Errorf("notice[%d] sub_sid = %s, want %s (FIFO order broken?)", i, got[i].SubSid, w.subSid)
				}
				if !strings.HasPrefix(got[i].Summary, w.prefix) {
					t.Errorf("notice[%d] prefix:\n%s\nwant prefix %s", i, got[i].Summary, w.prefix)
				}
				for _, frag := range w.contains {
					if !strings.Contains(got[i].Summary, frag) {
						t.Errorf("notice[%d] missing %q:\n%s", i, frag, got[i].Summary)
					}
				}
				for _, frag := range w.notContain {
					if strings.Contains(got[i].Summary, frag) {
						t.Errorf("notice[%d] must not contain %q:\n%s", i, frag, got[i].Summary)
					}
				}
			}
		})
	}
}

func dumpNotices(ns []BackgroundNotice) string {
	var sb strings.Builder
	for _, n := range ns {
		sb.WriteString(n.SubSid + ": " + n.Summary + "\n---\n")
	}
	return sb.String()
}

// TestRecoverNotices_NoIndexIsSteadyState: a project with no
// subagents.jsonl yet folds to an empty queue, not an error (fresh
// project resume).
func TestRecoverNotices_NoIndexIsSteadyState(t *testing.T) {
	withHome(t)
	project := t.TempDir()
	got, err := recoverNotices(project, "p1")
	if err != nil {
		t.Fatalf("recoverNotices on empty project: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("notices = %d, want 0", len(got))
	}
}

// newManagerOver builds a Manager over a caller-supplied project dir
// (the resume fixtures live there), mirroring newBackgroundManager's
// wiring. parentSid seeds ParentSidFn.
func newManagerOver(t *testing.T, project, parentSid string) *Manager {
	t.Helper()
	withHome(t)
	policy, err := permission.New(t.TempDir(), permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ManagerOpts{
		ProjectAbsPath:    project,
		ParentSidFn:       func() string { return parentSid },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return nil },
		Runner: func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
			return RunnerResult{Summary: "unused"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

// TestRecoverNotices_ManagerEnqueueDeliverAndIdempotency: the full D3
// resume chain over hand-crafted disk state — RecoverNotices
// enqueues, the Notifier delivers on the first Prompt-shaped
// OnPrePrompt AND writes the notified markers, the fold reads them
// back, and a second RecoverNotices (repeated resume) finds nothing
// left to re-inject. PRD 验收 #6/#7.
func TestRecoverNotices_ManagerEnqueueDeliverAndIdempotency(t *testing.T) {
	// Manager first: newManagerOver's withHome pins SEEK_HOME for the
	// whole test, so the fixtures written next resolve into the same
	// project root the Manager will fold.
	mgr := newManagerOver(t, t.TempDir(), "p-resume")
	project := mgr.opts.ProjectAbsPath
	writeIndexLines(t, project,
		`{"event":"started","sub_sid":"sub-done","parent_sid":"p-resume","ts":"2026-01-02T10:00:00Z","type":"explore","description":"nightly audit","background":true}`,
		`{"event":"completed","sub_sid":"sub-done","ts":"2026-01-02T10:05:00Z","tokens":{"prompt":700,"completion":90,"cache_hit":600}}`,
		`{"event":"started","sub_sid":"sub-orphan","parent_sid":"p-resume","ts":"2026-01-02T10:01:00Z","type":"explore","description":"crashed run","background":true}`,
		`{"event":"orphaned","sub_sid":"sub-orphan","ts":"2026-01-02T10:06:00Z"}`,
	)
	writeTranscript(t, project, "p-resume", "sub-done",
		trHeader, `{"role":"assistant","content":"audit clean, 0 findings"}`)

	// Turn-1 preamble: resume assembly enqueues the pending notices.
	n, err := mgr.RecoverNotices()
	if err != nil {
		t.Fatalf("RecoverNotices: %v", err)
	}
	if n != 2 {
		t.Fatalf("RecoverNotices = %d, want 2 (completed + orphaned)", n)
	}
	if got := len(mgr.PendingNotices()); got != 2 {
		t.Fatalf("pending after recover = %d, want 2", got)
	}

	// First Prompt after resume: the Notifier prepends both notices
	// as <context> messages and appends the notified markers.
	notifier := NewNotifier(mgr)
	out, err := notifier.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "continue from last night"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Prepend) != 2 {
		t.Fatalf("Prepend len = %d, want 2", len(out.Prepend))
	}
	// FIFO: completed at 10:05 lands before orphaned at 10:06.
	if !strings.Contains(out.Prepend[0].Content, "sub-sid=sub-done") ||
		!strings.Contains(out.Prepend[0].Content, "audit clean, 0 findings") {
		t.Errorf("prepend[0] (summary notice):\n%s", out.Prepend[0].Content)
	}
	if !strings.HasPrefix(strings.TrimPrefix(out.Prepend[0].Content, "<context>\n"),
		"[agent: background done sub-sid=sub-done]") {
		t.Errorf("prepend[0] missing done prefix:\n%s", out.Prepend[0].Content)
	}
	if !strings.Contains(out.Prepend[1].Content, "status=orphaned") {
		t.Errorf("prepend[1] (orphaned status note):\n%s", out.Prepend[1].Content)
	}
	for _, m := range out.Prepend {
		if !strings.HasSuffix(m.Content, "</context>") {
			t.Errorf("prepend missing </context>:\n%s", m.Content)
		}
		if m.Role != "user" {
			t.Errorf("prepend role = %s, want user", m.Role)
		}
	}

	// notified markers on disk, one per delivered notice.
	wantNotified := map[string]bool{"sub-done": false, "sub-orphan": false}
	for _, e := range indexEvents(t, mgr) {
		if e.Kind == "notified" {
			if _, ok := wantNotified[e.SubSid]; ok {
				wantNotified[e.SubSid] = true
			}
		}
	}
	for sid, seen := range wantNotified {
		if !seen {
			t.Errorf("no notified event on disk for %s", sid)
		}
	}

	// The fold reads the markers back (panel transparency: bg✓).
	subs, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range subs {
		if !s.Notified {
			t.Errorf("fold after delivery: %s Notified=false, want true", s.SubSid)
		}
	}

	// Repeated resume: nothing left to re-inject (幂等), and the
	// drained inbox stays empty (no double delivery).
	n2, err := mgr.RecoverNotices()
	if err != nil {
		t.Fatalf("second RecoverNotices: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second RecoverNotices = %d, want 0 (notified marker must prevent re-injection)", n2)
	}
	out2, _ := notifier.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "again"})
	if len(out2.Prepend) != 0 {
		t.Errorf("second OnPrePrompt delivered %d messages, want 0", len(out2.Prepend))
	}
}

// TestNotifier_MarksNotifiedOnLiveDelivery: the live (in-process) D2
// path writes the same notified marker at delivery — one write
// surface for both channels, so the resume fold can trust the marker
// regardless of which path delivered.
func TestNotifier_MarksNotifiedOnLiveDelivery(t *testing.T) {
	mgr, _ := newBackgroundManager(t, func(ctx context.Context, job RunnerJob) (RunnerResult, error) {
		return RunnerResult{
			Summary: "live findings",
			Tokens:  Tokens{Prompt: 100, Completion: 20, CacheHit: 0},
			Turns:   2,
		}, nil
	})
	res := mgr.SpawnBackground(SpawnArgs{Description: "live run", Prompt: "p", Type: TypeExplore})
	waitFor(t, 5*time.Second, func() bool { return len(mgr.PendingNotices()) == 1 })

	notifier := NewNotifier(mgr)
	out, err := notifier.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "next"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Prepend) != 1 {
		t.Fatalf("Prepend len = %d, want 1", len(out.Prepend))
	}
	found := false
	for _, e := range indexEvents(t, mgr) {
		if e.Kind == "notified" && e.SubSid == res.SubSid {
			found = true
		}
	}
	if !found {
		t.Errorf("no notified event on disk for live delivery %s", res.SubSid)
	}
	subs, err := mgr.List(ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || !subs[0].Notified {
		t.Errorf("fold after live delivery = %+v, want single Notified=true row", subs)
	}
}
