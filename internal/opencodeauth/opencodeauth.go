// Package opencodeauth obtains and stores OpenCode Console credentials
// through the browser device-authorization flow.
//
// Why this exists: the OpenCode Go lane's credential is a string the user
// used to copy out of a browser tab. The Console publishes an OAuth
// authorization server (.well-known/oauth-authorization-server) whose
// device grant lets a terminal client say "open this URL, approve" and
// receive a 30-day bearer token plus a refresh token — no copy-paste, and
// the consent page shows the client's registered name instead of
// "Unknown client" (which is what an unregistered client_id renders as).
//
// Contracts this package is built on (verified against the live service
// on 2026-11-19, each one necessary):
//
//   - Dynamic registration (POST /auth/oauth/register) accepts only
//     authorization_code + refresh_token. A device client is therefore
//     registered as an authorization_code client and the same client_id
//     is reused for the device grant — the device endpoint accepts it.
//   - POST /auth/device/code needs client_id (+ supports_org_scope) and
//     returns verification_uri_complete as a PATH ("/console/device?..."),
//     so callers must prefix the console origin.
//   - POST /auth/device/token takes grant_type=urn:ietf:params:oauth:
//     grant-type:device_code + device_code (+ client_id), and later
//     grant_type=refresh_token. Both return {access_token, refresh_token,
//     token_type:"Bearer", expires_in}.
//   - The access token authenticates the inference gateway AND the
//     Console API, but the gateway refuses it with 403 "Workspace
//     selection is required" unless x-org-id names a workspace. That id
//     comes from GET /api/user/workspaces with the same bearer.
//
// Credentials live in $SEEK_HOME/opencode-auth.json, mode 0600, separate
// from config.json: they are short-lived machine credentials, not user
// configuration, and `seek auth opencode --logout` must be able to delete
// them without touching the rest of the config.
package opencodeauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// Console endpoints. Overridable per Client for tests.
const (
	ConsoleBase     = "https://opencode.ai/console"
	RegisterPath    = "/auth/oauth/register"
	DeviceCodePath  = "/auth/device/code"
	DeviceTokenPath = "/auth/device/token"
	RevokePath      = "/auth/oauth/revoke"
	WorkspacesPath  = "/api/user/workspaces"
	ConsoleOrigin   = "https://opencode.ai"
)

// DeviceGrant is the RFC 8628 grant type the console uses.
const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// refreshSkew refreshes a little early so a token can't expire between the
// check and the request that uses it.
const refreshSkew = 5 * time.Minute

// Credentials is the persisted auth state. Adding fields is safe (older
// files simply lack them); removing one is not.
type Credentials struct {
	ClientID     string    `json:"client_id,omitempty"`
	ClientName   string    `json:"client_name,omitempty"`
	RegisteredAt time.Time `json:"registered_at,omitempty"`

	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`

	OrgID   string `json:"org_id,omitempty"`
	OrgName string `json:"org_name,omitempty"`
}

// Valid reports whether the credentials can plausibly authenticate a
// request (an access or refresh token is present).
func (c Credentials) Valid() bool {
	return c.AccessToken != "" || c.RefreshToken != ""
}

// Expiring reports whether the access token is missing or within the
// refresh skew.
func (c Credentials) Expiring(now time.Time) bool {
	return c.AccessToken == "" || now.Add(refreshSkew).After(c.ExpiresAt)
}

// Workspace is one console workspace (org) the account can use.
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Client performs the console flows. Zero value is NOT usable — use New.
type Client struct {
	// BaseURL is the console API origin (tests point it at httptest).
	BaseURL string
	// HTTP is the transport (nil → a 30s-timeout default).
	HTTP *http.Client
	// Out receives human-facing progress lines (nil → io.Discard).
	Out io.Writer
	// OpenBrowser launches a URL; nil disables auto-open (the URL is
	// always printed, so this is convenience only).
	OpenBrowser func(url string) error
	// Now and Sleep are test seams.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// PollInterval overrides the server-provided interval (tests).
	PollInterval time.Duration
}

// New returns a Client with production defaults.
func New() *Client {
	return &Client{
		BaseURL:     ConsoleBase,
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		Out:         io.Discard,
		OpenBrowser: OpenBrowser,
		Now:         time.Now,
		Sleep:       sleepCtx,
	}
}

func (c *Client) out() io.Writer {
	if c.Out == nil {
		return io.Discard
	}
	return c.Out
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return ConsoleBase
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return c.HTTP
}

func (c *Client) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

// UserAgent is "seek/<version>", the same shape the inference client
// sends. The console shows the DCR client_name on the consent page; the
// UA rides along so server logs can tell which binary registered it.
func UserAgent() string { return "seek/" + buildVersion() }

// ClientName is what the consent page renders. Registering under this
// name is what turns "Unknown client" into "seek/0.12.0".
//
// It is deliberately SHORTER than the UA: build info on a local build is
// a pseudo-version like "v0.12.1-0.20261119065534-cb27b90d2979+dirty",
// which is unreadable in a browser dialog. Keep major.minor.patch; fall
// back to "dev" when the version isn't a release tag (or isn't set).
func ClientName() string { return "seek/" + shortVersion(buildVersion()) }

func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if mv := info.Main.Version; mv != "" && mv != "(devel)" && !strings.HasPrefix(mv, "v0.0.0-") {
			return mv
		}
	}
	return "dev"
}

// shortVersion trims a Go module version to its major.minor.patch part:
// "v0.12.1-0.2026…+dirty" → "v0.12.1"; anything unrecognisable → "dev".
func shortVersion(v string) string {
	if !strings.HasPrefix(v, "v") {
		return "dev"
	}
	base := strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(base, "-+"); i >= 0 {
		base = base[:i]
	}
	segs := strings.Split(base, ".")
	if len(segs) < 3 {
		return "dev"
	}
	for _, s := range segs[:3] {
		if s == "" {
			return "dev"
		}
	}
	return "v" + strings.Join(segs[:3], ".")
}

// --- HTTP plumbing ---

type apiError struct {
	Status  int
	Tag     string // _tag: failure class ("OAuthRegistrationError")
	Code    string // error: OAuth code ("invalid_client_metadata")
	Message string
}

func (e *apiError) Error() string {
	// Both identifiers can carry signal: _tag names the failure class
	// ("OAuthRegistrationError"), error names the OAuth code
	// ("invalid_client_metadata"). Print both when present.
	head := e.Code
	if e.Tag != "" {
		if head != "" && head != e.Tag {
			head = e.Tag + " (" + head + ")"
		} else {
			head = e.Tag
		}
	}
	switch {
	case head != "" && e.Message != "":
		return fmt.Sprintf("opencode auth: %s: %s", head, e.Message)
	case head != "":
		return "opencode auth: " + head
	case e.Message != "":
		return "opencode auth: " + e.Message
	}
	return fmt.Sprintf("opencode auth: http %d", e.Status)
}

// isPending reports the "user hasn't approved yet" answer from the device
// token endpoint. The service has returned both an empty body and an
// authorization_pending error for this state, so both are treated as
// pending.
func isPending(e error) bool {
	var ae *apiError
	if !errors.As(e, &ae) {
		return false
	}
	if ae.Status != http.StatusBadRequest && ae.Status != http.StatusTooManyRequests {
		return false
	}
	switch ae.Code {
	case "", "authorization_pending", "slow_down":
		return true
	}
	return false
}

func isSlowDown(e error) bool {
	var ae *apiError
	return errors.As(e, &ae) && ae.Code == "slow_down"
}

func (c *Client) callJSON(ctx context.Context, method, path string, body, out any, bearer string) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("opencode auth: encode: %w", err)
		}
		rdr = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rdr)
	if err != nil {
		return fmt.Errorf("opencode auth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("opencode auth: http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return parseAPIError(resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("opencode auth: decode: %w", err)
	}
	return nil
}

func parseAPIError(status int, raw []byte) error {
	var env struct {
		Tag     string `json:"_tag"`
		Code    string `json:"error"`
		Message string `json:"message"`
		Desc    string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &env)
	msg := env.Message
	if msg == "" {
		msg = env.Desc
	}
	if env.Tag == "" && env.Code == "" && msg == "" {
		msg = strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
	}
	return &apiError{Status: status, Tag: env.Tag, Code: env.Code, Message: msg}
}

// --- flows ---

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	OrgID        string `json:"org_id"`
}

// EnsureClient registers a dynamic client the first time. Registration is
// idempotent from the user's perspective: the client_id is persisted and
// reused for every later login on this machine.
func (c *Client) EnsureClient(ctx context.Context, cred Credentials) (Credentials, error) {
	// Re-register when the display name changed (a version bump, or the
	// old full pseudo-version): the consent page caches nothing, so the
	// next login just corrects the name.
	if cred.ClientID != "" && cred.ClientName == ClientName() {
		return cred, nil
	}
	var resp Credentials
	err := c.callJSON(ctx, http.MethodPost, RegisterPath, map[string]any{
		"client_name":                ClientName(),
		"redirect_uris":              []string{"http://127.0.0.1:0/callback"},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}, &resp, "")
	if err != nil {
		return cred, err
	}
	if resp.ClientID == "" {
		return cred, fmt.Errorf("opencode auth: registration returned no client_id")
	}
	cred.ClientID = resp.ClientID
	// Always mirror the name we just registered with — leaving a stale
	// one behind makes every later login re-register (the trigger below
	// compares against ClientName) and prints the old name in --status.
	cred.ClientName = ClientName()
	cred.RegisteredAt = c.now().UTC()
	return cred, nil
}

type deviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Login runs the whole device flow: register (once) → device code →
// print/open the approval URL → poll → org discovery → return the
// credentials. It does not persist anything; the caller decides where
// and whether to save.
func (c *Client) Login(ctx context.Context, cred Credentials) (Credentials, error) {
	cred, err := c.EnsureClient(ctx, cred)
	if err != nil {
		return cred, err
	}
	var dev deviceCodeResponse
	if err := c.callJSON(ctx, http.MethodPost, DeviceCodePath, map[string]any{
		"client_id":          cred.ClientID,
		"supports_org_scope": false,
	}, &dev, ""); err != nil {
		return cred, err
	}
	if dev.DeviceCode == "" || dev.UserCode == "" {
		return cred, fmt.Errorf("opencode auth: device endpoint returned no code")
	}

	url := dev.VerificationURIComplete
	if url == "" {
		url = dev.VerificationURI
	}
	if strings.HasPrefix(url, "/") {
		url = ConsoleOrigin + url
	}
	fmt.Fprintf(c.out(), "\n  Open this in your browser to approve seek (%s):\n\n    %s\n\n", dev.UserCode, url)
	// The console's device page renders a HARD-CODED client_id allowlist
	// (opencode-cli / -console-cli / -desktop / -ios) and prints "Unknown
	// client" for everyone else — it never reads the DCR client_name we
	// register. Say so before the user opens the page, otherwise a
	// correct login looks broken.
	fmt.Fprintf(c.out(), "  Note: the approval page will call us an \"unknown client\" — that page only\n"+
		"        names OpenCode's own clients (%s). Approve it; the code above is ours.\n\n", dev.UserCode)
	if c.OpenBrowser != nil {
		if err := c.OpenBrowser(url); err != nil {
			fmt.Fprintf(c.out(), "  (could not open a browser automatically: %v)\n", err)
		}
	}

	interval := time.Duration(dev.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if c.PollInterval > 0 {
		interval = c.PollInterval
	}
	deadline := c.now().Add(time.Duration(max(dev.ExpiresIn, 60)) * time.Second)

	for {
		if c.now().After(deadline) {
			return cred, fmt.Errorf("opencode auth: timed out waiting for approval")
		}
		var tok tokenResponse
		err := c.callJSON(ctx, http.MethodPost, DeviceTokenPath, map[string]any{
			"grant_type":  DeviceGrant,
			"device_code": dev.DeviceCode,
			"client_id":   cred.ClientID,
		}, &tok, "")
		switch {
		case err == nil && tok.AccessToken != "":
			cred.AccessToken = tok.AccessToken
			cred.RefreshToken = tok.RefreshToken
			cred.ExpiresAt = c.now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC()
			if err := c.fillOrg(ctx, &cred); err != nil {
				return cred, err
			}
			return cred, nil
		case err == nil:
			return cred, fmt.Errorf("opencode auth: token response carried no access token")
		case isPending(err):
			if isSlowDown(err) {
				interval += 5 * time.Second
			}
		default:
			return cred, err
		}
		if err := c.sleep(ctx, interval); err != nil {
			return cred, err
		}
	}
}

// fillOrg discovers the workspace the token can bill. A single workspace
// is chosen silently; several are a hard error rather than a guess —
// billing the wrong team is not something to infer. A caller-supplied
// OrgID (the --org flag) is validated against the account's list so a
// typo fails here instead of as a 403 at inference time.
func (c *Client) fillOrg(ctx context.Context, cred *Credentials) error {
	list, err := c.Workspaces(ctx, cred.AccessToken)
	if err != nil {
		return err
	}
	if cred.OrgID != "" {
		for _, w := range list {
			if w.ID == cred.OrgID {
				cred.OrgName = w.Name
				return nil
			}
		}
		return fmt.Errorf("opencode auth: workspace %q is not one this account can use", cred.OrgID)
	}
	switch len(list) {
	case 0:
		return fmt.Errorf("opencode auth: this account has no workspace — subscribe to OpenCode Go first")
	case 1:
		cred.OrgID, cred.OrgName = list[0].ID, list[0].Name
		return nil
	default:
		names := make([]string, len(list))
		for i, w := range list {
			names[i] = fmt.Sprintf("%s (%s)", w.Name, w.ID)
		}
		return fmt.Errorf("opencode auth: account has %d workspaces — re-run with --org <id>: %s",
			len(list), strings.Join(names, ", "))
	}
}

// Workspaces lists the account's console workspaces.
func (c *Client) Workspaces(ctx context.Context, accessToken string) ([]Workspace, error) {
	var list []Workspace
	if err := c.callJSON(ctx, http.MethodGet, WorkspacesPath, nil, &list, accessToken); err != nil {
		return nil, err
	}
	return list, nil
}

// Refresh exchanges the refresh token for a fresh pair. The refresh token
// is single-use: the caller must persist the returned one.
func (c *Client) Refresh(ctx context.Context, cred Credentials) (Credentials, error) {
	if cred.RefreshToken == "" {
		return cred, fmt.Errorf("opencode auth: no refresh token — run `seek auth opencode` again")
	}
	var tok tokenResponse
	if err := c.callJSON(ctx, http.MethodPost, DeviceTokenPath, map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": cred.RefreshToken,
		"client_id":     cred.ClientID,
	}, &tok, ""); err != nil {
		return cred, err
	}
	if tok.AccessToken == "" {
		return cred, fmt.Errorf("opencode auth: refresh returned no access token")
	}
	cred.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		cred.RefreshToken = tok.RefreshToken
	}
	cred.ExpiresAt = c.now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC()
	if cred.OrgID == "" {
		if err := c.fillOrg(ctx, &cred); err != nil {
			return cred, err
		}
	}
	return cred, nil
}

// Revoke invalidates both tokens server-side. Best-effort by design: the
// local file is deleted regardless, and a network failure is reported but
// not fatal.
func (c *Client) Revoke(ctx context.Context, cred Credentials) error {
	var firstErr error
	for _, tok := range []string{cred.AccessToken, cred.RefreshToken} {
		if tok == "" {
			continue
		}
		if err := c.callJSON(ctx, http.MethodPost, RevokePath, map[string]any{
			"token":     tok,
			"client_id": cred.ClientID,
		}, nil, ""); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// refreshMu serialises Token within a process. Two goroutines that both
// see an expiring token would otherwise refresh with the same refresh
// token; the console treats a reused refresh token as theft and revokes
// the whole family, which is unrecoverable without a fresh browser login.
// (Found live: the catalogue refresh goroutine and the first prompt both
// refreshed at startup and killed the family.)
var refreshMu sync.Mutex

// Token returns a usable access token + the org header value, refreshing
// and persisting when the stored token is near expiry.
//
// Two layers of serialisation: refreshMu for goroutines in this process,
// and a lock file for other seek processes (TUI + `seek -p` + ACP
// commonly run side by side). The credentials are re-read after both
// locks are taken, so a refresh completed elsewhere is simply used.
func Token(ctx context.Context, c *Client) (token, orgID string, err error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	cred, ok := Load()
	if !ok || !cred.Valid() {
		return "", "", ErrNoCredentials
	}
	if cred.Expiring(c.now()) {
		release, lerr := lockCredentials()
		if lerr != nil {
			return "", "", lerr
		}
		defer release()
		if fresh, ok := Load(); ok && fresh.Valid() && !fresh.Expiring(c.now()) {
			cred = fresh
		} else {
			cred, err = c.Refresh(ctx, cred)
			if err != nil {
				return "", "", err
			}
			if err := Save(cred); err != nil {
				return "", "", err
			}
		}
	}
	if cred.OrgID == "" {
		return "", "", fmt.Errorf("opencode auth: no workspace recorded — run `seek auth opencode` again")
	}
	return cred.AccessToken, cred.OrgID, nil
}

// ErrNoCredentials is returned when no OAuth login exists on this machine.
var ErrNoCredentials = errors.New("opencode auth: not logged in (run `seek auth opencode`)")

// --- storage ---

// Path returns $SEEK_HOME/opencode-auth.json.
func Path() (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "opencode-auth.json"), nil
}

// Load reads the stored credentials. Missing or malformed files report
// ok=false — a corrupt credential file must degrade to "not logged in",
// never to a startup error.
func Load() (Credentials, bool) {
	p, err := Path()
	if err != nil {
		return Credentials{}, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Credentials{}, false
	}
	var cred Credentials
	if err := json.Unmarshal(raw, &cred); err != nil {
		return Credentials{}, false
	}
	return cred, true
}

// Save writes the credentials atomically with mode 0600. The rename keeps
// a crash mid-write from leaving a truncated token file behind.
func Save(cred Credentials) error {
	p, err := Path()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Clear deletes the credential file. Missing is success.
func Clear() error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// --- cross-process refresh lock ---

const (
	lockWait      = 15 * time.Second
	lockStale     = 60 * time.Second
	lockPollEvery = 100 * time.Millisecond
)

// lockCredentials takes an exclusive advisory lock next to the credential
// file. O_CREATE|O_EXCL is the one primitive every platform seek ships on
// has; a lock older than lockStale is assumed to be from a killed process
// and is stolen. The timeout is generous because the section it guards is
// a single HTTPS round trip, and failing to refresh would log the user
// out.
func lockCredentials() (func(), error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	lockPath := p + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("opencode auth: lock: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > lockStale {
			_ = os.Remove(lockPath) // stale: the holder died mid-refresh
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("opencode auth: another seek is refreshing the token (waited %s)", lockWait)
		}
		time.Sleep(lockPollEvery)
	}
}

// --- helpers ---

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// OpenBrowser opens url in the platform's default browser. Best-effort:
// callers print the URL regardless.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
