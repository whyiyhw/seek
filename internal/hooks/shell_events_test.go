package hooks

// P3 shell-event coverage (feature-orchestration §6 D15 /
// feature-hooks-injection §3.3): the five observer-only events —
// subagent_start / subagent_stop / stop / pre_compact / notification.
//
// Per acceptance: every observation point gets a triggered path (hook
// runs, env contract holds, audit row lands) and a not-triggered path
// (no config / other event fires → no exec, no audit row). Plus the
// two cross-cutting contracts:
//
//   - observer-only: timeout / non-zero exit log + audit + status but
//     never deny (there is no return value to deny through), and the
//     next hook in the chain still runs;
//   - audit shape: rows written by these events round-trip through
//     hooksconfig.ReadAuditLog — the exact reader web W4.6's
//     hooks/audit/tail uses — side by side with pre-M-O3.1 lines that
//     lack subagent_sid entirely.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooksconfig"
)

// fireP3Event invokes the runner method for event name ev.
func fireP3Event(r *ShellRunner, ev string) {
	switch ev {
	case hooksconfig.EventSubagentStart:
		r.OnSubagentStart(context.Background(), SubagentStartEvent{
			SID: "sub-42", Type: "code-reviewer", Desc: "review internal/hooks",
		})
	case hooksconfig.EventSubagentStop:
		r.OnSubagentStop(context.Background(), SubagentStopEvent{
			SID: "sub-42", Type: "code-reviewer", Desc: "review internal/hooks",
			Status: "completed", Turns: 3, Tokens: 654321,
		})
	case hooksconfig.EventStop:
		r.OnStop(context.Background(), StopEvent{Turns: 5, Finish: "stop"})
	case hooksconfig.EventPreCompact:
		r.OnPreCompact(context.Background(), PreCompactEvent{Trigger: "manual"})
	case hooksconfig.EventNotification:
		r.OnNotification(context.Background(), NotificationEvent{Kind: "os", Title: "seek: turn done"})
	}
}

// p3EventEnv is the event-specific env contract verbatim from
// feature-hooks-injection §3.3 (on top of the common SEEK_* base).
func p3EventEnv(ev string) map[string]string {
	switch ev {
	case hooksconfig.EventSubagentStart:
		return map[string]string{
			"SEEK_EVENT":         "subagent_start",
			"SEEK_SUBAGENT_SID":  "sub-42",
			"SEEK_SUBAGENT_TYPE": "code-reviewer",
			"SEEK_SUBAGENT_DESC": "review internal/hooks",
		}
	case hooksconfig.EventSubagentStop:
		return map[string]string{
			"SEEK_EVENT":           "subagent_stop",
			"SEEK_SUBAGENT_SID":    "sub-42",
			"SEEK_SUBAGENT_TYPE":   "code-reviewer",
			"SEEK_SUBAGENT_DESC":   "review internal/hooks",
			"SEEK_SUBAGENT_STATUS": "completed",
			"SEEK_SUBAGENT_TURNS":  "3",
			"SEEK_SUBAGENT_TOKENS": "654321",
		}
	case hooksconfig.EventStop:
		return map[string]string{
			"SEEK_EVENT":       "stop",
			"SEEK_STOP_TURNS":  "5",
			"SEEK_STOP_FINISH": "stop",
		}
	case hooksconfig.EventPreCompact:
		return map[string]string{
			"SEEK_EVENT":           "pre_compact",
			"SEEK_COMPACT_TRIGGER": "manual",
		}
	case hooksconfig.EventNotification:
		return map[string]string{
			"SEEK_EVENT":        "notification",
			"SEEK_NOTIFY_KIND":  "os",
			"SEEK_NOTIFY_TITLE": "seek: turn done",
		}
	}
	return nil
}

// oneP3Config builds a Config with a single hook on event ev (and
// nothing else).
func oneP3Config(ev string) hooksconfig.Config {
	h := hooksconfig.Hook{Name: "p3hook", Command: "p3 cmd", Source: hooksconfig.SourceUser, Event: ev}
	return hooksconfig.Config{
		SubagentStart: appendIf(ev == hooksconfig.EventSubagentStart, h),
		SubagentStop:  appendIf(ev == hooksconfig.EventSubagentStop, h),
		Stop:          appendIf(ev == hooksconfig.EventStop, h),
		PreCompact:    appendIf(ev == hooksconfig.EventPreCompact, h),
		Notification:  appendIf(ev == hooksconfig.EventNotification, h),
	}
}

func appendIf(cond bool, h hooksconfig.Hook) []hooksconfig.Hook {
	if cond {
		return []hooksconfig.Hook{h}
	}
	return nil
}

// TestP3Events_Triggered_EnvAndAudit is the triggered path for each of
// the five events: the hook execs once with the full env contract and
// the audit row lands with the right event / session / subagent_sid.
func TestP3Events_Triggered_EnvAndAudit(t *testing.T) {
	events := []string{
		hooksconfig.EventSubagentStart,
		hooksconfig.EventSubagentStop,
		hooksconfig.EventStop,
		hooksconfig.EventPreCompact,
		hooksconfig.EventNotification,
	}
	for _, ev := range events {
		t.Run(ev, func(t *testing.T) {
			fe := newFakeExec()
			fe.Set("p3 cmd", fakeResponse{Exit: 0})
			r, audit := newRunnerForTest(t, oneP3Config(ev), fe)

			fireP3Event(r, ev)
			_ = r.Close()

			calls := fe.Calls()
			// newRunnerForTest's OnSessionStart produced no hooks (config
			// has none for session_start), so exactly one exec: ours.
			if len(calls) != 1 {
				t.Fatalf("hook execs = %d, want 1", len(calls))
			}
			for k, v := range p3EventEnv(ev) {
				if got := calls[0].Env[k]; got != v {
					t.Errorf("env %s = %q, want %q", k, got, v)
				}
			}
			// Base env rides along too.
			if calls[0].Env["SEEK_SESSION_ID"] != "sess1" {
				t.Errorf("SEEK_SESSION_ID = %q, want sess1", calls[0].Env["SEEK_SESSION_ID"])
			}

			entries, err := hooksconfig.ReadAuditLog(audit.Path())
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("audit rows = %d, want 1: %+v", len(entries), entries)
			}
			e := entries[0]
			if e.Event != ev || e.Hook != "p3hook" || e.SessionID != "sess1" || e.ExitCode != 0 {
				t.Errorf("audit row = %+v", e)
			}
			// subagent_sid: set for the two subagent events, absent for
			// the rest (omitempty — the key must not appear at all).
			wantSID := ""
			switch ev {
			case hooksconfig.EventSubagentStart, hooksconfig.EventSubagentStop:
				wantSID = "sub-42"
			}
			if e.SubagentSID != wantSID {
				t.Errorf("audit subagent_sid = %q, want %q", e.SubagentSID, wantSID)
			}
		})
	}
}

// TestP3Events_NotTriggered: with no hooks configured for the event,
// dispatching it execs nothing and writes no audit row — and firing a
// DIFFERENT event does not run this event's hooks either.
func TestP3Events_NotTriggered(t *testing.T) {
	// A config that only has a stop hook; fire every OTHER event and
	// confirm the stop hook stays silent, then fire stop and confirm
	// it runs — the negative and positive control in one test.
	fe := newFakeExec()
	fe.Set("p3 cmd", fakeResponse{Exit: 0})
	r, audit := newRunnerForTest(t, oneP3Config(hooksconfig.EventStop), fe)

	for _, ev := range []string{
		hooksconfig.EventSubagentStart,
		hooksconfig.EventSubagentStop,
		hooksconfig.EventPreCompact,
		hooksconfig.EventNotification,
		// legacy events too — no cross-contamination either direction
		hooksconfig.EventPreTool,
		hooksconfig.EventPostTool,
		hooksconfig.EventPrePrompt,
		hooksconfig.EventSessionStart,
		hooksconfig.EventSessionEnd,
	} {
		fireP3Event(r, ev)
	}
	if n := len(fe.Calls()); n != 0 {
		t.Fatalf("execs after unrelated events = %d, want 0", n)
	}

	// Fully empty config: every P3 dispatch is a no-op.
	fe2 := newFakeExec()
	r2, audit2 := newRunnerForTest(t, hooksconfig.Config{}, fe2)
	for _, ev := range []string{
		hooksconfig.EventSubagentStart, hooksconfig.EventSubagentStop,
		hooksconfig.EventStop, hooksconfig.EventPreCompact, hooksconfig.EventNotification,
	} {
		fireP3Event(r2, ev)
	}
	_ = r2.Close()
	if n := len(fe2.Calls()); n != 0 {
		t.Errorf("execs with empty config = %d, want 0", n)
	}
	if entries, _ := hooksconfig.ReadAuditLog(audit2.Path()); len(entries) != 0 {
		t.Errorf("audit rows with empty config = %d, want 0", len(entries))
	}

	// Positive control: stop fires exactly once when it's the event.
	fireP3Event(r, hooksconfig.EventStop)
	_ = r.Close()
	if n := len(fe.Calls()); n != 1 {
		t.Errorf("execs after stop = %d, want 1", n)
	}
	if entries, _ := hooksconfig.ReadAuditLog(audit.Path()); len(entries) != 1 {
		t.Errorf("audit rows after stop = %d, want 1", len(entries))
	}
}

// TestP3Events_ObserverOnly_TimeoutAndFailure: non-zero exit and
// timeout surface as status events + audit rows but never veto
// anything, and the NEXT hook in the chain still runs (no
// short-circuit — only pre_tool denies).
func TestP3Events_ObserverOnly_TimeoutAndFailure(t *testing.T) {
	cfg := hooksconfig.Config{
		Stop: []hooksconfig.Hook{
			{Name: "boom", Command: "boom cmd", Source: hooksconfig.SourceUser, Event: hooksconfig.EventStop, TimeoutMs: 50},
			{Name: "next", Command: "next cmd", Source: hooksconfig.SourceUser, Event: hooksconfig.EventStop},
		},
	}
	fe := newFakeExec()
	fe.Set("boom cmd", fakeResponse{SleepFor: 200 * time.Millisecond}) // times out
	fe.Set("next cmd", fakeResponse{Exit: 3})                          // fails, observer-style

	var statuses []StatusEvent
	r, audit := newRunnerForTest(t, cfg, fe)
	r.onStatus = func(ev StatusEvent) { statuses = append(statuses, ev) }

	start := time.Now()
	r.OnStop(context.Background(), StopEvent{Turns: 1, Finish: "stop"})
	_ = r.Close()

	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("timeout not honored, took %s", d)
	}
	// Both hooks ran despite the first timing out and the second
	// failing — observer events never short-circuit.
	calls := fe.Calls()
	if len(calls) != 2 || calls[0].Command != "boom cmd" || calls[1].Command != "next cmd" {
		t.Fatalf("calls = %+v, want both hooks in order", calls)
	}
	if len(statuses) != 2 {
		t.Fatalf("status events = %d, want 2 (timeout + failure)", len(statuses))
	}
	if !statuses[0].TimedOut || statuses[0].Event != hooksconfig.EventStop {
		t.Errorf("status[0] = %+v, want timeout on stop", statuses[0])
	}
	if statuses[1].TimedOut || statuses[1].Event != hooksconfig.EventStop {
		t.Errorf("status[1] = %+v, want non-timeout failure on stop", statuses[1])
	}

	entries, err := hooksconfig.ReadAuditLog(audit.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(entries))
	}
	if !strings.Contains(entries[0].Reason, "timeout after 50ms") {
		t.Errorf("timeout audit reason = %q", entries[0].Reason)
	}
	if entries[1].ExitCode != 3 || entries[1].Denied {
		t.Errorf("failure audit row = %+v, want exit 3 and denied=false (observer events never deny)", entries[1])
	}
}

// TestP3Events_RegistryWiring: the runner satisfies the five new
// observer interfaces, so a single Register fans it into the new slots
// (this is the wiring main.go relies on — no per-event registration).
func TestP3Events_RegistryWiring(t *testing.T) {
	r := NewRegistry()
	runner := NewShellRunner(oneP3Config(hooksconfig.EventNotification))
	defer runner.Close()
	r.Register(runner)

	s := r.Stack()
	for slot, n := range map[string]int{
		"SubagentStart": 1, "SubagentStop": 1, "Stop": 1,
		"PreCompact": 1, "Notification": 1,
	} {
		got := map[string][]string{
			"SubagentStart": s.SubagentStart, "SubagentStop": s.SubagentStop,
			"Stop": s.Stop, "PreCompact": s.PreCompact, "Notification": s.Notification,
		}[slot]
		if len(got) != n {
			t.Errorf("Stack %s = %v, want %d entry", slot, got, n)
		}
	}
}

// TestP3AuditShape_W4_6ReaderCompatibility: audit rows from the new
// events must coexist with pre-M-O3.1 lines in the very file web
// W4.6's hooks/audit/tail reads via hooksconfig.ReadAuditLog. The
// first two lines below are copied shape-for-shape from the W4.6
// reader's own fixture (internal/rpc/methods/hooks_test.go
// auditFixture): no subagent_sid key at all. The runner then appends
// subagent rows; the reader must parse all of them.
func TestP3AuditShape_W4_6ReaderCompatibility(t *testing.T) {
	fe := newFakeExec()
	fe.Set("p3 cmd", fakeResponse{Exit: 0})
	r, audit := newRunnerForTest(t,
		hooksconfig.Config{
			SubagentStart: []hooksconfig.Hook{{Name: "bg-audit", Command: "p3 cmd", Source: hooksconfig.SourceUser, Event: hooksconfig.EventSubagentStart}},
			SubagentStop:  []hooksconfig.Hook{{Name: "bg-lint", Command: "p3 cmd", Source: hooksconfig.SourceUser, Event: hooksconfig.EventSubagentStop}},
		}, fe)

	// Pre-existing lines in the old wire shape (verbatim W4.6 fixture
	// forms — no subagent_sid field).
	oldLines := []string{
		`{"ts":"2026-11-15T20:00:00Z","event":"pre_tool","hook":"alpha-lint","tool":"edit","session_id":"sess-old","duration_ms":12,"exit_code":0,"denied":false}`,
		`{"ts":"2026-11-15T22:00:00Z","event":"pre_tool","hook":"alpha-lint","tool":"bash","session_id":"sess-old","duration_ms":30,"exit_code":1,"denied":true}`,
	}
	if err := writeAuditLines(audit.Path(), oldLines); err != nil {
		t.Fatal(err)
	}

	r.OnSubagentStart(context.Background(), SubagentStartEvent{SID: "sub-7", Type: "explorer", Desc: "d"})
	r.OnSubagentStop(context.Background(), SubagentStopEvent{SID: "sub-7", Status: "failed", Turns: 2, Tokens: 100})
	_ = r.Close()

	entries, err := hooksconfig.ReadAuditLog(audit.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 (2 old + 2 new): %+v", len(entries), entries)
	}
	// Old rows parse with an empty subagent_sid (the field they
	// predate) and their original values intact.
	for i, want := range []struct {
		hook, tool string
		denied     bool
	}{
		{"alpha-lint", "edit", false},
		{"alpha-lint", "bash", true},
	} {
		e := entries[i]
		if e.Hook != want.hook || e.Tool != want.tool || e.Denied != want.denied {
			t.Errorf("old row %d = %+v, want hook=%s tool=%s denied=%v", i, e, want.hook, want.tool, want.denied)
		}
		if e.SubagentSID != "" {
			t.Errorf("old row %d grew a subagent_sid: %q", i, e.SubagentSID)
		}
	}
	// New rows carry subagent_sid and the new event names.
	if entries[2].Event != hooksconfig.EventSubagentStart || entries[2].SubagentSID != "sub-7" {
		t.Errorf("subagent_start row = %+v", entries[2])
	}
	if entries[3].Event != hooksconfig.EventSubagentStop || entries[3].SubagentSID != "sub-7" {
		t.Errorf("subagent_stop row = %+v", entries[3])
	}
}

// writeAuditLines appends raw lines to the audit file behind an
// already-open AuditLog (O_APPEND keeps the handle and the manual
// writes coherent).
func writeAuditLines(path string, lines []string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			return err
		}
	}
	return nil
}
