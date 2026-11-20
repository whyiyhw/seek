package controlplane

// Settings-panel tests (W5 card c_ni7mzw): the read-only serve-side
// snapshot the page's ⚙ panel renders, plus the about block's token
// short display. The panel is READ-ONLY by card scope — there is no
// write method to test, so these tests pin the read contract instead:
// what the method returns, that it never leaks the token, and that a
// frontend with no snapshot says so instead of inventing values.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWeb_SettingsGet(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fe.SetSettings(ServeSettings{
		Version:         "v0.9.0 · abc1234",
		DefaultProvider: "deepseek",
		MaxConcurrency:  6,
		Yolo:            true,
		WebAddr:         "127.0.0.1:18790",
		Projects:        []string{"/tmp/alpha", "/tmp/beta"},
		AutoCompact:     "350000 tokens",
	})
	fe.RegisterSettingsMethods(fe.RPCRegistry())

	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"settings/get","params":{}}`
	resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /rpc = %d, want 200", resp.StatusCode)
	}
	var env struct {
		Result struct {
			Version         string   `json:"version"`
			TokenTail       string   `json:"token_tail"`
			DefaultProvider string   `json:"default_provider"`
			MaxConcurrency  int      `json:"max_concurrency"`
			Yolo            bool     `json:"yolo"`
			WebAddr         string   `json:"web_addr"`
			Projects        []string `json:"projects"`
			AutoCompact     string   `json:"auto_compact"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil {
		t.Fatalf("settings/get errored: %+v", env.Error)
	}
	r := env.Result
	if r.Version != "v0.9.0 · abc1234" || r.DefaultProvider != "deepseek" ||
		r.MaxConcurrency != 6 || !r.Yolo || r.WebAddr != "127.0.0.1:18790" ||
		r.AutoCompact != "350000 tokens" || len(r.Projects) != 2 {
		t.Errorf("settings snapshot drifted: %+v", r)
	}
	// About block: the token's LAST 6 chars only — never the whole
	// secret, and never its head (hashes/tokens share prefixes least
	// often at the tail... this is a display aid, not a proof).
	const tok = "test-token-0123456789abcdef"
	if r.TokenTail != tok[len(tok)-tokenTailLen:] {
		t.Errorf("token_tail = %q, want the last %d chars of the live token", r.TokenTail, tokenTailLen)
	}
	if strings.Contains(r.TokenTail, "test-token") {
		t.Errorf("token_tail leaks the token's head: %q", r.TokenTail)
	}
}

// A bare frontend (tests/embedders: no snapshot handed in) must answer
// an explicit error — the page then renders "settings unavailable"
// instead of a card full of zeros that reads like real config.
func TestWeb_SettingsUnavailable(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fe.RegisterSettingsMethods(fe.RPCRegistry())

	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":7,"method":"settings/get","params":{}}`
	resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil {
		t.Fatal("settings/get on a snapshot-less frontend returned a result, want an error")
	}
	if env.Error.Code != -32603 || !strings.Contains(env.Error.Message, "settings unavailable") {
		t.Errorf("error = %+v, want -32603 with an explicit 'settings unavailable' message", env.Error)
	}
}

// Non-object params are a client bug worth naming (the family's
// convention), not something to silently ignore.
func TestWeb_SettingsBadParams(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fe.SetSettings(ServeSettings{Version: "dev"})
	fe.RegisterSettingsMethods(fe.RPCRegistry())

	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":9,"method":"settings/get","params":"nope"}`
	resp, err := http.Post(srv.URL+"/rpc?token=test-token-0123456789abcdef", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil || env.Error.Code != -32602 {
		t.Errorf("scalar params = %+v, want -32602", env.Error)
	}
}
