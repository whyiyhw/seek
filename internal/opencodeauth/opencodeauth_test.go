package opencodeauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeConsole is a stand-in for the console API. Each test tunes the
// fields it cares about; everything else answers the happy path.
type fakeConsole struct {
	t *testing.T

	mu sync.Mutex

	registerStatus  int
	registerName    string // client_name seen by the server
	deviceStatus    int
	pendingPolls    int // how many device/token polls answer "pending" first
	deviceFailBody  string
	tokenStatus     int
	workspaces      []Workspace
	workspacesError bool
	revoked         []string
	refreshed       int
	lastRefreshTok  string
	expiresIn       int // default 2592000
	openedURLs      []string
}

func newFakeConsole(t *testing.T) (*fakeConsole, *httptest.Server) {
	f := &fakeConsole{t: t, workspaces: []Workspace{{ID: "org_1", Name: "Personal"}}, expiresIn: 2592000}
	mux := http.NewServeMux()

	mux.HandleFunc(RegisterPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.registerName, _ = body["client_name"].(string)
		status := f.registerStatus
		f.mu.Unlock()
		if status != 0 && status != 200 && status != 201 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"_tag":"OAuthRegistrationError","error":"invalid_client_metadata","error_description":"nope"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"client_id":"oac_test","client_name":"` + f.registerName + `","token_endpoint_auth_method":"none"}`))
	})

	mux.HandleFunc(DeviceCodePath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		status := f.deviceStatus
		f.mu.Unlock()
		if status != 0 && status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"_tag":"DeviceAuthFailed","message":"Device authorization failed"}`))
			return
		}
		if body["client_id"] != "oac_test" {
			f.t.Errorf("device/code client_id = %v, want the registered one", body["client_id"])
		}
		_, _ = w.Write([]byte(`{"device_code":"dev_1","user_code":"ABCD-EFGH",` +
			`"verification_uri":"/console/device","verification_uri_complete":"/console/device?user_code=ABCD-EFGH&client_id=oac_test",` +
			`"expires_in":600,"interval":1}`))
	})

	mux.HandleFunc(DeviceTokenPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		grant, _ := body["grant_type"].(string)

		f.mu.Lock()
		defer f.mu.Unlock()
		switch grant {
		case DeviceGrant:
			if f.tokenStatus != 0 && f.tokenStatus != 200 {
				w.WriteHeader(f.tokenStatus)
				_, _ = w.Write([]byte(f.deviceFailBody))
				return
			}
			if f.pendingPolls > 0 {
				f.pendingPolls--
				// The live service has answered an empty body here; the
				// client must read that as "keep waiting".
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(f.deviceFailBody))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"st_1","refresh_token":"rt_1","token_type":"Bearer","expires_in":` +
				strconv.Itoa(f.expiresIn) + `}`))
		case "refresh_token":
			if f.tokenStatus != 0 && f.tokenStatus != 200 {
				w.WriteHeader(f.tokenStatus)
				_, _ = w.Write([]byte(f.deviceFailBody))
				return
			}
			f.refreshed++
			f.lastRefreshTok, _ = body["refresh_token"].(string)
			_, _ = w.Write([]byte(`{"access_token":"st_2","refresh_token":"rt_2","token_type":"Bearer","expires_in":2592000}`))
		default:
			f.t.Errorf("unexpected grant_type %q", grant)
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	mux.HandleFunc(WorkspacesPath, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer st_1" && got != "Bearer st_2" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"_tag":"Unauthorized"}`))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.workspacesError {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(f.workspaces)
	})

	mux.HandleFunc(RevokePath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.revoked = append(f.revoked, body["token"])
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeConsole) client(srv *httptest.Server) *Client {
	c := New()
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	c.PollInterval = time.Millisecond
	c.OpenBrowser = func(u string) error {
		f.mu.Lock()
		f.openedURLs = append(f.openedURLs, u)
		f.mu.Unlock()
		return nil
	}
	return c
}

func TestLogin_DeviceFlow(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	f.pendingPolls = 2 // two "not approved yet" answers before the token
	c := f.client(srv)
	var out bytes.Buffer
	c.Out = &out

	cred, err := c.Login(context.Background(), Credentials{})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if cred.ClientID != "oac_test" {
		t.Errorf("client_id = %q, want the registered one", cred.ClientID)
	}
	if f.registerName != ClientName() {
		t.Errorf("registered client_name = %q, want %q (consent page shows this)", f.registerName, ClientName())
	}
	// The stored name must mirror the registration, otherwise the next
	// login re-registers forever and --status shows a stale name.
	if cred.ClientName != ClientName() {
		t.Errorf("stored client_name = %q, want %q", cred.ClientName, ClientName())
	}
	if cred.AccessToken != "st_1" || cred.RefreshToken != "rt_1" {
		t.Errorf("tokens = %q/%q", cred.AccessToken, cred.RefreshToken)
	}
	if cred.OrgID != "org_1" || cred.OrgName != "Personal" {
		t.Errorf("org = %q/%q, want the single workspace", cred.OrgID, cred.OrgName)
	}
	if len(f.openedURLs) != 1 || !strings.HasPrefix(f.openedURLs[0], ConsoleOrigin+"/console/device") {
		t.Errorf("opened URLs = %v, want one console-origin device URL", f.openedURLs)
	}
	if !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Errorf("progress output missing the user code:\n%s", out.String())
	}
}

func TestLogin_ReusesRegisteredClient(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	c := f.client(srv)

	cred := Credentials{ClientID: "oac_test", ClientName: ClientName()}
	if _, err := c.Login(context.Background(), cred); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if f.registerName != "" {
		t.Error("login must not re-register when a client_id is already stored")
	}

	// A stale name (older binary) does re-register — and the stored name
	// is corrected in the same pass.
	cred.ClientName = "seek/v0.0.0-old"
	updated, err := c.Login(context.Background(), cred)
	if err != nil {
		t.Fatalf("Login after name change: %v", err)
	}
	if f.registerName != ClientName() {
		t.Errorf("re-registration used %q, want %q", f.registerName, ClientName())
	}
	if updated.ClientName != ClientName() {
		t.Errorf("stored name after re-registration = %q", updated.ClientName)
	}
}

func TestLogin_MultipleWorkspacesNeedsOrg(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	f.workspaces = []Workspace{{ID: "org_1", Name: "Personal"}, {ID: "org_2", Name: "Team"}}
	c := f.client(srv)

	cred, err := c.Login(context.Background(), Credentials{})
	if err == nil || !strings.Contains(err.Error(), "--org") {
		t.Fatalf("err = %v, want the multi-workspace hint", err)
	}
	if cred.AccessToken == "" {
		t.Error("tokens should still be returned so the caller can retry with --org")
	}

	// Explicit org is validated, then used.
	cred, err = c.Login(context.Background(), Credentials{OrgID: "org_2"})
	if err != nil {
		t.Fatalf("Login with --org: %v", err)
	}
	if cred.OrgID != "org_2" || cred.OrgName != "Team" {
		t.Errorf("org = %q/%q, want org_2/Team", cred.OrgID, cred.OrgName)
	}

	if _, err := c.Login(context.Background(), Credentials{OrgID: "org_nope"}); err == nil {
		t.Error("unknown workspace id must fail at login, not at inference time")
	}
}

func TestLogin_RejectsBadRegistrationAndDeviceCode(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	c := f.client(srv)

	f.registerStatus = http.StatusBadRequest
	if _, err := c.Login(context.Background(), Credentials{}); err == nil ||
		!strings.Contains(err.Error(), "invalid_client_metadata") {
		t.Errorf("registration failure = %v, want the server error surfaced", err)
	}

	f.registerStatus = 0
	f.deviceStatus = http.StatusBadRequest
	if _, err := c.Login(context.Background(), Credentials{}); err == nil {
		t.Error("device/code failure must abort the login")
	}
}

func TestLogin_AccessDenied(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	f.tokenStatus = http.StatusBadRequest
	f.deviceFailBody = `{"error":"access_denied","error_description":"user said no"}`
	c := f.client(srv)

	_, err := c.Login(context.Background(), Credentials{})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("err = %v, want access_denied surfaced (not treated as pending)", err)
	}
}

func TestLogin_ContextCancelStopsPolling(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	f.pendingPolls = 1 << 30 // never approves
	c := f.client(srv)
	c.PollInterval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if _, err := c.Login(ctx, Credentials{}); err == nil {
		t.Error("cancelled context must abort the poll loop")
	}
}

func TestToken_NoCredentials(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	c := New()
	if _, _, err := Token(context.Background(), c); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("err = %v, want ErrNoCredentials", err)
	}
}

func TestToken_RefreshesNearExpiryAndPersists(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	c := f.client(srv)
	c.Now = func() time.Time { return time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC) }

	// Expires in 2 minutes → inside the 5-minute skew → must refresh.
	cred := Credentials{
		ClientID: "oac_test", AccessToken: "st_old", RefreshToken: "rt_old",
		ExpiresAt: c.Now().Add(2 * time.Minute), OrgID: "org_1",
	}
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}

	token, org, err := Token(context.Background(), c)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if token != "st_2" || org != "org_1" {
		t.Errorf("token/org = %q/%q, want the refreshed pair", token, org)
	}
	if f.refreshed != 1 || f.lastRefreshTok != "rt_old" {
		t.Errorf("refresh calls = %d, sent %q", f.refreshed, f.lastRefreshTok)
	}
	stored, ok := Load()
	if !ok || stored.AccessToken != "st_2" || stored.RefreshToken != "rt_2" {
		t.Errorf("refreshed pair not persisted: %+v ok=%v", stored, ok)
	}
}

func TestToken_FreshTokenSkipsNetwork(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	c := f.client(srv)
	c.BaseURL = "http://127.0.0.1:1" // would fail if anything dialled out
	c.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	cred := Credentials{
		AccessToken: "st_1", RefreshToken: "rt_1",
		ExpiresAt: c.Now().Add(10 * time.Hour), OrgID: "org_1",
	}
	if err := Save(cred); err != nil {
		t.Fatal(err)
	}
	token, _, err := Token(context.Background(), c)
	if err != nil || token != "st_1" {
		t.Errorf("token = %q err = %v, want the stored one without a refresh", token, err)
	}
}

func TestToken_RefreshFailureSurfaces(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	f.tokenStatus = http.StatusUnauthorized
	f.deviceFailBody = `{"error":"invalid_grant","error_description":"expired"}`
	c := f.client(srv)
	cred := Credentials{
		ClientID: "oac_test", AccessToken: "st_old", RefreshToken: "rt_old",
		ExpiresAt: time.Now().Add(-time.Hour), OrgID: "org_1",
	}
	if err := Save(cred); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Token(context.Background(), c); err == nil {
		t.Error("a failed refresh must surface, not silently return a dead token")
	}
}

// TestToken_ConcurrentRefreshesOnce is the regression guard for the bug
// that killed a live token family: several goroutines seeing an expiring
// token must produce exactly ONE refresh. A second refresh would reuse
// the (now rotated) refresh token, which the console treats as theft and
// answers by revoking the whole family.
func TestToken_ConcurrentRefreshesOnce(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	c := f.client(srv)
	c.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }

	if err := Save(Credentials{
		ClientID: "oac_test", AccessToken: "st_old", RefreshToken: "rt_old",
		ExpiresAt: c.Now().Add(-time.Minute), OrgID: "org_1",
	}); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	tokens := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], _, errs[i] = Token(context.Background(), c)
		}(i)
	}
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if tokens[i] != "st_2" {
			t.Errorf("goroutine %d token = %q, want the refreshed one", i, tokens[i])
		}
	}
	if f.refreshed != 1 {
		t.Errorf("refresh calls = %d, want exactly 1 (reuse revokes the family server-side)", f.refreshed)
	}
}

// TestToken_LockIsHonouredAndStale: a live lock file makes a second
// refresher wait (here: time out quickly), a stale one is stolen.
func TestToken_LockIsHonouredAndStale(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	if _, err := Path(); err != nil {
		t.Fatal(err)
	}
	release, err := lockCredentials()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	// A second attempt must block; make the wait short by shrinking the
	// deadline through a stale-looking file instead of sleeping 15s.
	p, _ := Path()
	if err := os.Chtimes(p+".lock", time.Now().Add(-2*lockStale), time.Now().Add(-2*lockStale)); err != nil {
		t.Fatal(err)
	}
	release2, err := lockCredentials()
	if err != nil {
		t.Fatalf("stale lock should be stolen: %v", err)
	}
	release2()
	release()
	if _, err := os.Stat(p + ".lock"); !os.IsNotExist(err) {
		t.Errorf("lock file left behind: %v", err)
	}
}

func TestRevoke_ServerAndLocal(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	c := f.client(srv)
	cred := Credentials{ClientID: "oac_test", AccessToken: "st_1", RefreshToken: "rt_1"}
	if err := Save(cred); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := doLogout(context.Background(), c, &out); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if len(f.revoked) != 2 {
		t.Errorf("revoked %v, want both tokens", f.revoked)
	}
	if _, ok := Load(); ok {
		t.Error("logout must delete the credential file")
	}
}

func TestRevoke_BestEffortWhenServerFails(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	f, srv := newFakeConsole(t)
	c := f.client(srv)
	c.BaseURL = "http://127.0.0.1:1" // unreachable: revoke will error
	if err := Save(Credentials{ClientID: "oac_test", AccessToken: "st_1"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := doLogout(context.Background(), c, &out); err != nil {
		t.Fatalf("logout must not fail on a network error: %v", err)
	}
	if !strings.Contains(out.String(), "revoke failed") {
		t.Errorf("output = %q, want the warning", out.String())
	}
	if _, ok := Load(); ok {
		t.Error("local token must be deleted even when the revoke call fails")
	}
}

func TestStore_PermissionsAndCorruption(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	if _, ok := Load(); ok {
		t.Error("missing file must report ok=false")
	}
	cred := Credentials{ClientID: "oac_test", AccessToken: "st_1", RefreshToken: "rt_1"}
	if err := Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p, _ := Path()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(home, "opencode-auth.json.tmp")); err == nil {
		t.Error("temp file left behind")
	}

	if err := os.WriteFile(p, []byte("{ nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(); ok {
		t.Error("corrupt file must degrade to not-logged-in")
	}
	if err := Clear(); err != nil {
		t.Errorf("Clear: %v", err)
	}
	if err := Clear(); err != nil {
		t.Errorf("Clear on a missing file must be a no-op: %v", err)
	}
}

func TestCLI_StatusLogoutAndUsage(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	var out, errW bytes.Buffer

	if err := Run(nil, &out, &errW); err == nil {
		t.Error("no args must be a usage error")
	}
	if err := Run([]string{"github"}, &out, &errW); err == nil {
		t.Error("unknown target must be a usage error")
	}

	out.Reset()
	errW.Reset()
	if err := Run([]string{"opencode", "--status"}, &out, &errW); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "Not logged in") {
		t.Errorf("status output = %q", out.String())
	}

	if err := Save(Credentials{ClientID: "oac_test", ClientName: "seek/test", AccessToken: "st_1",
		RefreshToken: "rt_1", ExpiresAt: time.Now().Add(48 * time.Hour), OrgID: "org_1", OrgName: "Personal"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run([]string{"opencode", "--status"}, &out, &errW); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"seek/test", "Personal", "org_1", "valid"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := Run([]string{"opencode", "--logout"}, &out, &errW); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, ok := Load(); ok {
		t.Error("--logout must delete the file")
	}
}
