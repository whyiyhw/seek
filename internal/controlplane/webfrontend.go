package controlplane

// Web frontend (MVP, card c_qucxtu): drive seek from a browser chat
// page. Default build, stdlib only — the D-F1 SDK-isolation gate exists
// to keep frontend SDKs out of the shipped binary, and net/http IS the
// stdlib, so unlike feishu this adapter needs no build tag.
//
// Shape: one fixed conversation ("web:default" — single-user v1, D-C5;
// per-tab conversations are future work). The page is a go:embed'ed
// single HTML file (webfrontend.html, zero build chain); commands like
// /bind pass through as plain text and reuse the M2 router verbatim.
//
//	GET  /        the embedded page (no token — it holds no secrets;
//	              the token reaches the page via its own URL)
//	GET  /events  SSE downstream: every Send broadcast to all tabs
//	POST /msg     upstream: {"text":"..."} → one Inbound into the plane
//	GET  /state   the web conversation's binding, for the status line
//
// Token posture mirrors butler's /today precedent (localhost / tailnet
// audience): a per-install random token in ~/.seek/serve/web.token,
// accepted as `?token=` (EventSource cannot set headers) or
// `Authorization: Bearer`. Comparison is constant-time. The full UI
// (auth, multi-tab sync, history) is deliberately out of MVP scope —
// design card c_7pptol owns that.
//
// Known MVP limitation, by design: Send with no connected SSE client
// drops the reply (chat-history persistence is out of scope; seek runs
// take seconds, SSE connects in milliseconds, the race is negligible).

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

//go:embed webfrontend.html
var webPage []byte

// webConvNative is the single conversation native id: every browser tab
// is the same conversation. Single-user v1; per-tab ids would multiply
// bindings for zero MVP value (D-C5 — Sender is unused too).
const webConvNative = "default"

// webConvID is the fixed ConvID ("web:default") the frontend speaks.
func webConvID() ConvID { return NewConvID("web", webConvNative) }

// DefaultWebAddr is the listen address of the web frontend. Loopback
// only: the intended audience is this machine and the tailnet (which
// reaches loopback via a TCP forward), never the public internet.
const DefaultWebAddr = "127.0.0.1:8790"

// WebConfig tunes WebFrontend. The zero value is the production shape:
// DefaultWebAddr, ~/.seek/serve/web.token and the default binding store.
// Tests inject temp paths.
type WebConfig struct {
	Addr      string    // listen address; "" → DefaultWebAddr
	TokenPath string    // token file; "" → <ServeDir>/web.token
	StorePath string    // binding store path; "" → <ServeDir>/bindings.json
	Banner    io.Writer // startup line ("web up: <url>"); nil → os.Stderr
}

// WebFrontend is the browser-chat Frontend. Safe for concurrent use:
// Run from the serve goroutine, Send from executor goroutines, HTTP
// handlers from the server's goroutines.
type WebFrontend struct {
	cfg    WebConfig
	cfgErr error // deferred WebConfig resolution failure, if any

	store *BindingStore

	tokenOnce sync.Once
	token     string
	tokenErr  error

	inboxMu sync.RWMutex
	inbox   chan<- Inbound // set by Run; handlers 503 before that

	mu   sync.Mutex
	subs map[int]chan string // SSE subscribers: buffered event-json chans
	next int
}

// Compile-time port check (the fake asserts the same contract).
var _ Frontend = (*WebFrontend)(nil)

// NewWeb builds the frontend, resolving zero WebConfig fields against
// the serve dir. Resolution errors are held and surfaced by Run (the
// registry constructor cannot fail, so failure stays lazy-but-loud).
func NewWeb(cfg WebConfig) *WebFrontend {
	if cfg.Addr == "" {
		cfg.Addr = DefaultWebAddr
	}
	if cfg.Banner == nil {
		cfg.Banner = os.Stderr
	}
	f := &WebFrontend{cfg: cfg}
	if cfg.TokenPath == "" || cfg.StorePath == "" {
		dir, err := paths.ServeDir()
		if err != nil {
			f.cfgErr = fmt.Errorf("web: resolve serve dir: %w", err)
			return f
		}
		if cfg.TokenPath == "" {
			f.cfg.TokenPath = filepath.Join(dir, "web.token")
		}
		if cfg.StorePath == "" {
			f.cfg.StorePath = filepath.Join(dir, "bindings.json")
		}
	}
	// This store handle is read-only (Get for /state); the router's own
	// handle owns all writes. Same process, atomic renames on Put — a
	// reader can at worst see the previous snapshot.
	f.store = OpenBindingStoreAt(f.cfg.StorePath)
	return f
}

// Name implements the Frontend port.
func (f *WebFrontend) Name() string { return "web" }

// ensureToken loads the token, generating + persisting a fresh one on
// first run. Sticky: the first error (or success) is remembered.
func (f *WebFrontend) ensureToken() (string, error) {
	f.tokenOnce.Do(func() {
		if b, err := os.ReadFile(f.cfg.TokenPath); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
			f.token = strings.TrimSpace(string(b))
			return
		}
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			f.tokenErr = fmt.Errorf("web: generate token: %w", err)
			return
		}
		tok := hex.EncodeToString(raw)
		if err := os.MkdirAll(filepath.Dir(f.cfg.TokenPath), 0o700); err != nil {
			f.tokenErr = fmt.Errorf("web: create serve dir: %w", err)
			return
		}
		// 0600, same posture as the token files butler keeps.
		if err := os.WriteFile(f.cfg.TokenPath, []byte(tok), 0o600); err != nil {
			f.tokenErr = fmt.Errorf("web: write token file: %w", err)
			return
		}
		f.token = tok
	})
	return f.token, f.tokenErr
}

// auth enforces the token on every stateful route: Authorization:
// Bearer first, then ?token= (EventSource cannot set headers — the
// butler /today precedent). Constant-time compare either way.
func (f *WebFrontend) auth(r *http.Request) bool {
	tok, err := f.ensureToken()
	if err != nil {
		return false
	}
	got := r.URL.Query().Get("token")
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(tok)) == 1
}

// handler builds the route table. Unexported so tests drive it via
// httptest without a listener; Run is the only production caller.
func (f *WebFrontend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.handlePage)
	mux.HandleFunc("/events", f.handleEvents)
	mux.HandleFunc("/msg", f.handleMsg)
	mux.HandleFunc("/state", f.handleState)
	return mux
}

// Run implements the Frontend port: listen, serve, park until ctx is
// done, then force-close (SSE connections never go idle, so a graceful
// Shutdown would block forever; at plane shutdown in-flight POST bodies
// are tiny and the Serve drain owns the queue, not HTTP responses).
func (f *WebFrontend) Run(ctx context.Context, in chan<- Inbound) error {
	if f.cfgErr != nil {
		return f.cfgErr
	}
	tok, err := f.ensureToken()
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", f.cfg.Addr)
	if err != nil {
		return fmt.Errorf("web: listen %s: %w", f.cfg.Addr, err)
	}
	f.inboxMu.Lock()
	f.inbox = in
	f.inboxMu.Unlock()

	srv := &http.Server{Handler: f.handler()} // Addr unused: serving on ln
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	fmt.Fprintf(f.cfg.Banner, "web: up: http://%s/?token=%s\n", f.cfg.Addr, tok)

	<-ctx.Done()
	srv.Close()
	if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web: serve: %w", err)
	}
	return nil
}

// Send implements the Frontend port: broadcast one reply to every
// connected SSE client (all tabs are the same conversation). Best-effort
// — a full subscriber buffer drops that event for that client, a Send
// with no subscribers is a silent no-op (documented MVP limitation).
func (f *WebFrontend) Send(ctx context.Context, conv ConvID, r Reply) error {
	ev, err := json.Marshal(map[string]string{"conv": string(conv), "text": r.Text})
	if err != nil {
		return err // unreachable for string maps, but the type says it
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs {
		select {
		case ch <- string(ev):
		default: // slow client: drop the event, never block the executor
		}
	}
	return nil
}

// ---- HTTP handlers ----

func (f *WebFrontend) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(webPage)
}

func (f *WebFrontend) handleMsg(w http.ResponseWriter, r *http.Request) {
	if !f.auth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token (use ?token= or Authorization: Bearer)"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var body struct{ Text string }
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	if err := dec.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {\"text\":\"...\"}"})
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is empty"})
		return
	}
	f.inboxMu.RLock()
	in := f.inbox
	f.inboxMu.RUnlock()
	if in == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "plane not running"})
		return
	}
	// EventID stays empty: browsers do not redeliver, and the router's
	// contract says empty ids are never deduped — distinct messages
	// cannot be collapsed. DirectAt is always true: a browser page IS
	// the direct audience; there is no group-chatter shape here.
	select {
	case in <- Inbound{Conv: webConvID(), Text: body.Text, Sender: "web", DirectAt: true}:
	case <-r.Context().Done():
		return // client gave up; the message was never accepted
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (f *WebFrontend) handleState(w http.ResponseWriter, r *http.Request) {
	if !f.auth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
		return
	}
	b, err := f.store.Get(webConvID())
	if err != nil && !errors.Is(err, ErrBindingNotFound) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bound":   err == nil,
		"project": b.ProjectPath,
		"session": b.SessionID,
	})
}

// handleEvents is the SSE downstream. One goroutine per tab: replay
// nothing, stream replies + a 15s heartbeat (keeps intermediaries from
// idling the conn; `retry:` makes the browser reconnect after a serve
// restart), exit on client disconnect or write error.
func (f *WebFrontend) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !f.auth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	io.WriteString(w, "retry: 3000\n\n") // browser auto-reconnect, 3s
	fl.Flush()

	f.mu.Lock()
	if f.subs == nil {
		f.subs = make(map[int]chan string)
	}
	f.next++
	ch := make(chan string, 16)
	f.subs[f.next] = ch
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.subs, f.next)
		f.mu.Unlock()
	}()

	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		select {
		case ev := <-ch:
			if _, err := io.WriteString(w, "data: "+ev+"\n\n"); err != nil {
				return // client went away
			}
			fl.Flush()
		case <-beat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
