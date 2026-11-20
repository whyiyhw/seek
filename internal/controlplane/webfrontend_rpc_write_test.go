package controlplane

// Route-level permission test for the W5 session write pair
// (c_rqcuaa acceptance ①'s 无权限 leg): /rpc's §5.2 write gate
// (cookie alone drives nothing) must cover session/rename and
// session/archive exactly as it covers approval/decide — a
// cookie-authenticated POST without a same-origin Origin (or
// X-Seek-Token) answers 403 AND leaves the meta state untouched,
// while the token face reaches the real family and lands the
// mutation. The generic table (TestWeb_RPCWriteGate) drives a test
// method; this one proves the real family rides the same route.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/rpc/methods"
	"github.com/whyiyhw/seek/internal/sessmeta"
	"github.com/whyiyhw/seek/internal/session"
)

func TestWeb_RPCSessionWritePermission(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	dir := t.TempDir()
	store, err := session.NewStoreAt(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sid := "20261120-090000-permtest0000000"
	if err := store.Save(&session.Session{SchemaVersion: 3, ID: sid, Model: "deepseek-chat", CWD: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	meta := sessmeta.OpenAt(filepath.Join(dir, "meta.json"), filepath.Join(dir, "audit.jsonl"))
	methods.NewSessionsWithMeta(store, meta).RegisterAll(fe.RPCRegistry())
	srv := httptest.NewServer(fe.handler())
	t.Cleanup(srv.Close)

	post := func(url, cookie, origin string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, url,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"session/rename","params":{"sid":"`+sid+`","title":"越权改名"}}`))
		if cookie != "" {
			req.Header.Set("Cookie", webTokenCookie+"="+cookie)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	// Cookie alone (no Origin, no X-Seek-Token): 403, mutation blocked.
	if code, body := post(srv.URL+"/rpc", tok, ""); code != http.StatusForbidden || strings.Contains(body, "result") {
		t.Fatalf("cookie-only rename: status=%d body=%q, want 403 with no result member", code, body)
	}
	if code, _ := post(srv.URL+"/rpc", tok, "http://attacker.example"); code != http.StatusForbidden {
		t.Fatalf("cross-site Origin rename: status=%d, want 403", code)
	}
	all, err := meta.All()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all[sid]; ok {
		t.Fatal("denied rename must leave the meta store untouched")
	}

	// Token face (the script/first-hop shape): the mutation lands.
	if code, body := post(srv.URL+"/rpc?token="+tok, "", ""); code != http.StatusOK || !strings.Contains(body, `"title":"越权改名"`) {
		t.Fatalf("token rename: status=%d body=%q, want 200 with the stored title", code, body)
	}
	all, _ = meta.All()
	if all[sid].Title != "越权改名" {
		t.Fatalf("token rename must land, got %v", all[sid])
	}
}
