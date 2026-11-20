package methods

// session/rename + session/archive tests (W5 c_rqcuaa acceptance ①,
// all sandbox: writeFixture's temp store + a temp sessmeta — no real
// home): normal round-trips through session/list, concurrent renames,
// archive idempotency/unarchive, and the error tiers (missing session
// -32001, bad sid/title -32602, unwired meta -32603). The
// cookie-without-Origin permission leg is route-level and lives in
// controlplane (TestWeb_RPCSessionWritePermission) — methods have no
// auth surface of their own.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/sessmeta"
)

// writeFamily builds the fixture store + meta pair wired through a
// registry.
func writeFamily(t *testing.T) (*rpc.Registry, *sessmeta.Store) {
	t.Helper()
	store := writeFixture(t)
	dir := t.TempDir()
	meta := sessmeta.OpenAt(filepath.Join(dir, "meta.json"), filepath.Join(dir, "audit.jsonl"))
	reg := rpc.NewRegistry()
	NewSessionsWithMeta(store, meta).RegisterAll(reg)
	return reg, meta
}

func listTitles(t *testing.T, reg *rpc.Registry, params string) map[string]sessionSummary {
	t.Helper()
	res, err := reg.Dispatch(context.Background(), "session/list", json.RawMessage(params))
	if err != nil {
		t.Fatalf("session/list: %v", err)
	}
	out := map[string]sessionSummary{}
	for _, s := range res.(sessionListResult).Sessions {
		out[s.SID] = s
	}
	return out
}

func dispatchCode(t *testing.T, reg *rpc.Registry, method, params string) (any, int) {
	t.Helper()
	res, err := reg.Dispatch(context.Background(), method, json.RawMessage(params))
	if err != nil {
		var re *rpc.Error
		if !errors.As(err, &re) {
			t.Fatalf("%s: non-coded error %v", method, err)
		}
		return nil, re.Code
	}
	return res, 0
}

func TestSessionRename(t *testing.T) {
	reg, meta := writeFamily(t)

	// Normal: rename lands in the meta store AND the list view.
	res, code := dispatchCode(t, reg, "session/rename", `{"sid":"`+mainSID+`","title":"  主线改名\n第二行被拍平 "}`)
	if code != 0 {
		t.Fatalf("rename code = %d, want 0", code)
	}
	if got := res.(sessionRenameResult).Title; got != "主线改名" {
		t.Fatalf("stored title = %q, want multi-line flattened+trimmed 主线改名", got)
	}
	rows := listTitles(t, reg, `{}`)
	if got := rows[mainSID].Title; got != "主线改名" {
		t.Fatalf("list title = %q, want the override", got)
	}
	if got := rows[branchSID].Title; got != "what if we add tests?" {
		t.Fatalf("untouched session title = %q, want derived", got)
	}
	all, _ := meta.All()
	if all[mainSID].Title != "主线改名" {
		t.Fatal("rename must persist in the meta store")
	}

	// Error tiers.
	for _, tc := range []struct{ name, params string; want int }{
		{"missing session", `{"sid":"` + unknownSID + `","title":"x"}`, rpc.CodeSessionNotFound},
		{"empty title", `{"sid":"` + mainSID + `","title":"   "}`, rpc.CodeInvalidParams},
		{"too long title", `{"sid":"` + mainSID + `","title":"` + strings.Repeat("长", 81) + `"}`, rpc.CodeInvalidParams},
		{"traversal sid", `{"sid":"../evil","title":"x"}`, rpc.CodeInvalidParams},
		{"no sid", `{"title":"x"}`, rpc.CodeInvalidParams},
	} {
		if _, code := dispatchCode(t, reg, "session/rename", tc.params); code != tc.want {
			t.Errorf("%s: code = %d, want %d", tc.name, code, tc.want)
		}
	}
}

// TestSessionRenameConcurrent: acceptance ①'s 并发 leg at the method
// layer — parallel dispatches (same sid last-writer-wins AND distinct
// sids all landing) leave the meta state consistent and the list
// answering one of the written titles.
func TestSessionRenameConcurrent(t *testing.T) {
	reg, meta := writeFamily(t)
	sids := []string{mainSID, branchSID, emptySID}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sid := sids[i%len(sids)]
			_, _ = reg.Dispatch(context.Background(), "session/rename",
				json.RawMessage(`{"sid":"`+sid+`","title":"并发改名 `+string(rune('A'+i%26))+`"}`))
		}(i)
	}
	wg.Wait()
	all, err := meta.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(sids) {
		t.Fatalf("distinct overridden sids = %d, want %d (a torn merge lost entries)", len(all), len(sids))
	}
	rows := listTitles(t, reg, `{}`)
	for _, sid := range sids {
		if !strings.HasPrefix(rows[sid].Title, "并发改名 ") {
			t.Errorf("sid %s title = %q, want one of the concurrent renames", sid, rows[sid].Title)
		}
	}
}

func TestSessionArchive(t *testing.T) {
	reg, _ := writeFamily(t)

	// Archive → hidden by default, visible with include_archived.
	res, code := dispatchCode(t, reg, "session/archive", `{"sid":"`+emptySID+`"}`)
	if code != 0 {
		t.Fatalf("archive code = %d", code)
	}
	if r := res.(sessionArchiveResult); !r.Archived || !r.Changed {
		t.Fatalf("first archive = %+v, want archived+changed", r)
	}
	if _, ok := listTitles(t, reg, `{}`)[emptySID]; ok {
		t.Fatal("archived session must be hidden from the default list")
	}
	rows := listTitles(t, reg, `{"include_archived":true}`)
	if !rows[emptySID].Archived {
		t.Fatal("include_archived must surface the row with archived=true")
	}

	// Idempotent: second archive → changed=false, state untouched.
	res, code = dispatchCode(t, reg, "session/archive", `{"sid":"`+emptySID+`","archived":true}`)
	if code != 0 {
		t.Fatalf("second archive code = %d", code)
	}
	if r := res.(sessionArchiveResult); r.Changed {
		t.Fatal("second archive must answer changed=false (幂等)")
	}

	// Unarchive (explicit false — the pointer must survive the wire).
	res, code = dispatchCode(t, reg, "session/archive", `{"sid":"`+emptySID+`","archived":false}`)
	if code != 0 {
		t.Fatalf("unarchive code = %d", code)
	}
	if r := res.(sessionArchiveResult); r.Archived || !r.Changed {
		t.Fatalf("unarchive = %+v, want archived=false changed=true", r)
	}
	if _, ok := listTitles(t, reg, `{}`)[emptySID]; !ok {
		t.Fatal("unarchived session must return to the default list")
	}

	// Error tiers.
	for _, tc := range []struct{ name, params string; want int }{
		{"missing session", `{"sid":"` + unknownSID + `"}`, rpc.CodeSessionNotFound},
		{"traversal sid", `{"sid":"a/b"}`, rpc.CodeInvalidParams},
		{"no sid", `{}`, rpc.CodeInvalidParams},
	} {
		if _, code := dispatchCode(t, reg, "session/archive", tc.params); code != tc.want {
			t.Errorf("%s: code = %d, want %d", tc.name, code, tc.want)
		}
	}
}

// TestSessionsNoMetaReadOnly: the NewSessions seam keeps the W4.2
// read behaviour — list works without a meta store (byte-shape: no
// archived field), the write pair fails -32603 instead of silently
// no-op'ing.
func TestSessionsNoMetaReadOnly(t *testing.T) {
	store := writeFixture(t)
	reg := rpc.NewRegistry()
	NewSessions(store).RegisterAll(reg)
	rows := listTitles(t, reg, `{}`)
	if len(rows) != 3 {
		t.Fatalf("list = %d rows, want 3", len(rows))
	}
	if _, code := dispatchCode(t, reg, "session/rename", `{"sid":"`+mainSID+`","title":"x"}`); code != rpc.CodeInternalError {
		t.Errorf("rename without meta: code = %d, want -32603", code)
	}
	if _, code := dispatchCode(t, reg, "session/archive", `{"sid":"`+mainSID+`"}`); code != rpc.CodeInternalError {
		t.Errorf("archive without meta: code = %d, want -32603", code)
	}
}
