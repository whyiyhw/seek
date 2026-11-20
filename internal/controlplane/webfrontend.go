package controlplane

// Web frontend (MVP, card c_qucxtu): drive seek from a browser chat
// page. Default build, stdlib only — the D-F1 SDK-isolation gate exists
// to keep frontend SDKs out of the shipped binary, and net/http IS the
// stdlib, so unlike feishu this adapter needs no build tag.
//
// Shape: per-conversation addressing (W1.3, webfrontend_conv.go) — the
// page mints a uuid (crypto.randomUUID, localStorage) and carries it as
// X-Seek-Conversation; requests without it address the legacy default
// conv "web:default" (D-W3 compat, bindings stay untouched). The page
// is a go:embed'ed single HTML file (webfrontend.html, zero build
// chain); commands like /bind pass through as plain text and reuse the
// M2 router verbatim.
//
//	GET  /        the embedded page (no auth — it holds no secrets; a
//	              valid ?token= never renders: the first hop 303-
//	              exchanges it for an HttpOnly cookie, §5.1 W1.2)
//	GET  /static/ the page's asset family (W5, c_x2au4t: markdown.js
//	              is the first non-inline resident; same 'self' CSP,
//	              no auth — same no-secrets posture as the page)
//	GET  /events  SSE downstream: Send broadcast to the conv's tabs
//	              (?conv= — EventSource cannot set headers); ?topic=
//	              opts the connection into a topic stream alongside
//	              (W3.2: "subagents" — see webfrontend_topic.go)
//	POST /msg     upstream: {"text":"..."} → one Inbound into the plane
//	              (X-Seek-Conversation picks the conv)
//	POST /attach  one attachment's raw bytes (?name=<filename>) → staged
//	              under the serve dir; the page turns the path into an
//	              "@<path>" token in its /msg text, so the child's
//	              existing image/document routers keep owning capability
//	              judgement (webfrontend_attach.go)
//	GET  /state   the requesting conv's binding, for the status line
//	POST /rpc     JSON-RPC 2.0 asset-plane calls (W4.1 envelope; the
//	              session method set registers at servecli assembly —
//	              W4.2 — NewWeb itself registers nothing; see
//	              webfrontend_rpc.go)
//	GET  /subagents  the conv's bound-project subagent state projection
//	              folded from subagents.jsonl (?parent=<sid> filter;
//	              W3.1 — see webfrontend_subagents.go)
//	POST /voice      push-to-talk audio upload (V2, voicefrontend.go —
//	              mounted only when a VoiceFrontend Attached pre-Run)
//
// Approval cards (W5 c_n37tnr) ride the CONV stream as the named SSE
// event `approval` (not the ?topic= dimension — that one is for the
// file-follow backed panels; a card must pop in every view, and the
// main /events connection is the always-on carrier, voice precedent).
// Decisions go through POST /rpc approval/decide — see
// webfrontend_approval.go for the whole contract.
//
// Token posture mirrors butler's /today precedent (localhost / tailnet
// audience): a per-install random token in ~/.seek/serve/web.token,
// accepted as `?token=` (EventSource cannot set headers) or
// `Authorization: Bearer`; since W1.2 also as the HttpOnly cookie the
// /?token= first hop exchanges (webfrontend_auth.go carries the whole
// credential/gate stack: authKind, writeGate, gateHost). Comparison is
// constant-time on every branch. The full UI (auth, multi-tab sync,
// history) is deliberately out of MVP scope — design card c_7pptol
// owns that.
//
// Known MVP limitation, by design: Send with no connected SSE client
// drops the reply (chat-history persistence is out of scope; seek runs
// take seconds, SSE connects in milliseconds, the race is negligible).

import (
	"context"
	"crypto/rand"
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
	"github.com/whyiyhw/seek/internal/rpc"
)

//go:embed webfrontend.html
var webPage []byte

// webConvNative is the LEGACY default conversation: pre-W1.3 clients
// sent no conv id and every tab shared this one conversation. It stays
// as the address for header-less requests so existing web:default
// bindings keep working (D-W3 compat: not migrated, not deleted).
const webConvNative = "default"

// webConvID is that default ConvID ("web:default").
func webConvID() ConvID { return NewConvID("web", webConvNative) }

// DefaultWebAddr is the listen address of the web frontend. Loopback
// only: the intended audience is this machine and the tailnet (which
// reaches loopback via a TCP forward), never the public internet.
const DefaultWebAddr = "127.0.0.1:8790"

// tailnetCGNAT is the IPv4 block Tailscale assigns tailnet IPs from
// (CGNAT 100.64.0.0/10, RFC 6598). Binding there serves the tailnet
// only — WireGuard-gated, never the public internet.
var tailnetCGNAT = net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0).To4(),
	Mask: net.CIDRMask(10, 32),
}

// checkWebAddr gates WebConfig.Addr to the two audiences the frontend
// is built for: this machine (loopback — 127.0.0.1/::1/localhost, i.e.
// all of 127/8) and the tailnet (CGNAT 100.64.0.0/10). Everything
// else — 0.0.0.0/::/empty host (all interfaces), LAN and public IPs,
// arbitrary hostnames — is refused up front (2026-11-15 user decision,
// c_x7ehom rework): the frontend bears a token that drives seek runs
// against bound projects, so "listens everywhere" must not be one
// typo away. Syntax stays the listen's business: an address
// net.SplitHostPort cannot cut falls through and dies at Run's listen
// with its own (authoritative) error.
func checkWebAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil // unparseable: Run's listen is the syntax authority
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || tailnetCGNAT.Contains(ip) {
			return nil
		}
	} else if strings.EqualFold(host, "localhost") {
		return nil
	}
	return fmt.Errorf("web: web_addr %q: only loopback (127.0.0.1/::1/localhost) and tailnet CGNAT 100.64.0.0/10 may listen — bind this machine's 100.x.y.z for tailnet access, or keep 127.0.0.1 and forward with `tailscale serve --tcp=8790`", addr)
}

// WebConfig tunes WebFrontend. The zero value is the production shape:
// DefaultWebAddr, ~/.seek/serve/web.token and the default binding store.
// Tests inject temp paths.
type WebConfig struct {
	Addr      string    // listen address; "" → DefaultWebAddr; scope-gated by checkWebAddr (loopback + tailnet CGNAT only)
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

	// attachDir is where POST /attach stages uploaded bytes
	// (webfrontend_attach.go): <ServeDir>/attachments. Derived next to
	// the token/bindings paths so a test-injected store keeps uploads
	// in its own temp tree.
	attachDir string

	// rpcReg serves POST /rpc (W4.1). Built empty in NewWeb, registered
	// into only before Run — the §3.2 phase contract; handlers dispatch.
	rpcReg *rpc.Registry

	// voiceMount (V2 push-to-talk, voicefrontend.go): when Attach sets
	// it, the /voice upload endpoint rides this mux — same listener,
	// same auth/CSP/host-gate wrap. Mounted at most once, strictly
	// before Run builds the mux (the §3.2 phase contract again).
	voiceMount http.HandlerFunc

	tokenOnce sync.Once
	token     string
	tokenErr  error

	inboxMu sync.RWMutex
	inbox   chan<- Inbound // set by Run; handlers 503 before that

	mu   sync.Mutex
	subs map[ConvID]map[int]chan sseFrame // per-conv SSE subscribers (W1.3): buffered frame chans
	next int                              // subscriber ids, unique across convs
	runs map[ConvID]*webRun               // live streamed run per conv (W1.4); guarded by mu

	// Approval hub (W5 c_n37tnr, webfrontend_approval.go): pending and
	// recently-terminal cards keyed by request id, plus the terminal
	// eviction FIFO. Guarded by mu like subs/runs — the hub is hub
	// state.
	approvals   map[string]*webApproval
	approvalDone []string

	// Topic dimension (W3.2, webfrontend_topic.go): the same /events
	// connections' second channel (drop-oldest buffer) hung on the
	// shared per-index-path subagents.jsonl tail-follows behind the
	// "subagents" topic — the FOLLOW is the fanout unit, so pushes
	// reach only that project's subscribers. Guarded by mu like subs.
	follows map[string]*topicFollow
}

// Compile-time port check (the fake asserts the same contract).
var _ Frontend = (*WebFrontend)(nil)

// NewWeb builds the frontend, resolving zero WebConfig fields against
// the serve dir and scope-gating Addr (checkWebAddr). Resolution and
// scope errors are held and surfaced by Run (the registry constructor
// cannot fail, so failure stays lazy-but-loud).
func NewWeb(cfg WebConfig) *WebFrontend {
	if cfg.Addr == "" {
		cfg.Addr = DefaultWebAddr
	}
	if cfg.Banner == nil {
		cfg.Banner = os.Stderr
	}
	f := &WebFrontend{cfg: cfg}
	// The registry is built before the scope gate so RPCRegistry() is
	// never nil even on the cfgErr path — pre-Run method wiring
	// (W4.2+) must not depend on the address surviving the gate; a
	// gated-out frontend fails at Run with cfgErr regardless.
	f.rpcReg = rpc.NewRegistry()
	// Scope gate before any filesystem work: an out-of-scope address
	// must fail with the allowlist reason, not a token-file error.
	if err := checkWebAddr(cfg.Addr); err != nil {
		f.cfgErr = err
		return f
	}
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
	// Attachments live BESIDE the config pair, never at the real
	// ~/.seek (a test/CLI override moves all three together).
	f.attachDir = filepath.Join(filepath.Dir(f.cfg.StorePath), attachDirName)
	// This store handle is read-only (Get for /state); the router's own
	// handle owns all writes. Same process, atomic renames on Put — a
	// reader can at worst see the previous snapshot.
	f.store = OpenBindingStoreAt(f.cfg.StorePath)
	return f
}

// RPCRegistry exposes the /rpc method table for pre-Run wiring (the
// method sets of W4.2/W4.3). Registration after Run races the
// handlers' Dispatch — the §3.2 contract forbids it.
func (f *WebFrontend) RPCRegistry() *rpc.Registry { return f.rpcReg }

// mountVoice hosts the voice frontend's upload handler at /voice.
// Pre-Run only: handler() builds the route table once inside Run.
func (f *WebFrontend) mountVoice(h http.HandlerFunc) { f.voiceMount = h }

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

// handler builds the route table. Unexported so tests drive it via
// httptest without a listener; Run is the only production caller. The
// Host allowlist wraps the whole table (§5.3, webfrontend_auth.go) —
// rebinding defense sits outside every route, auth included. Inside
// it, the W1.5 asset pipeline (webfrontend_static.go) wraps every
// route twice: CSP on each response, gzip on the textual ones (SSE
// passes through uncompressed — flushing beats compressing there).
func (f *WebFrontend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.handlePage)
	mux.HandleFunc("/static/", f.handleStatic) // W5 asset family (markdown.js — c_x2au4t)
	mux.HandleFunc("/events", f.handleEvents)
	mux.HandleFunc("/msg", f.handleMsg)
	mux.HandleFunc("/attach", f.handleAttach) // W5: attachment intake (webfrontend_attach.go)
	mux.HandleFunc("/state", f.handleState)
	mux.HandleFunc("/rpc", f.handleRPC)
	mux.HandleFunc("/subagents", f.handleSubagents)
	if f.voiceMount != nil {
		mux.HandleFunc("/voice", f.voiceMount)
	}
	return gateHost(withCSP(withGzip(mux)))
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

// Send implements the Frontend port: broadcast one reply to every SSE
// client subscribed to conv — and only conv (W1.3 isolation; tabs of
// the same conv still share one stream). Best-effort — a full
// subscriber buffer drops that event for that client, a Send with no
// subscribers is a silent no-op (documented MVP limitation).
//
// W1.4 close-out: when a streamed run is live on conv, the reply
// envelope carries that run's id (+ session_id from agent_end) so
// clients replace the streaming bubble with the authoritative text —
// a PURE addition over the MVP {"conv","text"} shape (webReplyEvent);
// with no live run (command acks, pre-stream frontends) the envelope
// stays byte-identical to the MVP marshal.
func (f *WebFrontend) Send(ctx context.Context, conv ConvID, r Reply) error {
	run := f.popRun(conv)
	var data []byte
	var err error
	if run != nil {
		data, err = json.Marshal(webReplyEvent{Conv: string(conv), Text: r.Text, Run: run.id, SessionID: run.sessionID})
	} else {
		data, err = json.Marshal(map[string]string{"conv": string(conv), "text": r.Text})
	}
	if err != nil {
		return err // unreachable for these payloads, but the type says it
	}
	f.broadcast(conv, sseFrame{data: string(data)})
	return nil
}

// ---- HTTP handlers ----

func (f *WebFrontend) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// First-hop token exchange (W1.2, §5.1): a VALID ?token= on the
	// browser entry never renders the page with the token in the URL —
	// 303 to a bare / with the token moved into an HttpOnly cookie, so
	// the address bar, history and referer stay clean. Wrong/absent
	// tokens fall through and serve the page: its fetches then 401 and
	// the status line says so (a garbage token is scrubbed from
	// nothing — it was never the secret).
	if tok := r.URL.Query().Get("token"); tok != "" && f.authKind(r) == credParam {
		f.setTokenCookie(w, r, tok)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(webPage)
}

func (f *WebFrontend) handleMsg(w http.ResponseWriter, r *http.Request) {
	// Write surface, §5.2 (W1.2): the token'd forms pass as before;
	// a cookie-authenticated request additionally clears the CSRF bar
	// (same-origin Origin or X-Seek-Token) — a cookie alone drives
	// nothing. 403, not 401: the client IS authenticated, it is just
	// not write-cleared.
	switch f.authKind(r) {
	case credNone:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token (use ?token= or Authorization: Bearer)"})
		return
	case credCookie:
		if !f.writeGate(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cookie-authenticated writes need a same-origin Origin header or X-Seek-Token"})
			return
		}
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	conv, err := f.convFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
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
	case in <- Inbound{Conv: conv, Text: body.Text, Sender: "web", DirectAt: true}:
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
	conv, err := f.convFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b, err := f.store.Get(conv)
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
// nothing, stream the conv's frames — the MVP default-event replies
// plus the W1.4 named-event family delta/tool/usage and the approval
// family (W5 c_n37tnr; EventSource
// delivers named events only to addEventListener, so the pre-W1.4
// page's onmessage is untouched) — and a 15s heartbeat (keeps
// intermediaries from idling the conn; `retry:` makes the browser
// reconnect after a serve restart), exit on client disconnect or write
// error. Every event is written and flushed immediately (§10: a proxy
// must never batch it). The conv comes from ?conv= (EventSource cannot
// set headers).
func (f *WebFrontend) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !f.auth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
		return
	}
	conv, err := f.convFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Topic dimension (W3.2): ?topic= opts this connection into a
	// topic stream alongside the conv stream. Validated and resolved
	// BEFORE the SSE headers so a bad topic or unbound conv answers
	// plain 400/500 JSON, never a broken event-stream.
	topic := r.URL.Query().Get("topic")
	var indexPath string
	if topic != "" {
		if topic != topicSubagents {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown topic " + topic + " (only \"subagents\" exists)"})
			return
		}
		b, err := f.store.Get(conv)
		if err != nil {
			if errors.Is(err, ErrBindingNotFound) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "conversation not bound to a project (send /bind <path> first)"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		indexPath, err = paths.SubagentsIndex(b.ProjectPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Proxy hint (§10 risk table): tell nginx & co. not to buffer this
	// stream — every event must reach the client the moment it is
	// written (per-event Flush below is the other half).
	w.Header().Set("X-Accel-Buffering", "no")
	io.WriteString(w, "retry: 3000\n\n") // browser auto-reconnect, 3s
	fl.Flush()

	var (
		ch    chan sseFrame
		tch   chan string // nil without a topic: a nil chan blocks forever, one select serves both paths
		unsub func()
	)
	if topic != "" {
		ch, tch, unsub = f.subscribeTopic(conv, indexPath)
	} else {
		ch, unsub = f.subscribe(conv)
	}
	defer unsub()

	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		select {
		case fr := <-ch:
			if fr.name != "" { // named event (W1.4 family); "" = MVP default
				if _, err := io.WriteString(w, "event: "+fr.name+"\n"); err != nil {
					return // client went away
				}
			}
			if _, err := io.WriteString(w, "data: "+fr.data+"\n\n"); err != nil {
				return // client went away
			}
			fl.Flush()
		case ev := <-tch:
			if _, err := io.WriteString(w, "event: "+topicSubagents+"\ndata: "+ev+"\n\n"); err != nil {
				return
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
