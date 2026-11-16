package controlplane

// Per-conversation addressing for the web frontend (W1.3, card
// c_nl4nqg; feature-web-frontend §2.3 D-W3). The MVP addressed one
// fixed conversation ("web:default"); this file adds the pieces that
// key every request to a browser-held conversation instead:
//
//   - convFromRequest: X-Seek-Conversation header (or ?conv= —
//     EventSource cannot set headers, same reason the token also has a
//     query form). No conv on the request → the legacy default conv,
//     so pre-W1.3 clients and their existing web:default binding keep
//     working unchanged (D-W3 compat: nothing migrated, nothing
//     deleted).
//   - the per-conv SSE hub: subscribe/broadcast below replace the
//     MVP's one flat subscriber table, so Send reaches only the
//     conversation's own tabs (multi-tab fanout within one conv is
//     unchanged — every subscriber of that conv).
//
// Server-side session state stays ZERO (D-W3): a conv id is only ever
// a bindings-table key; the browser owns the uuid (crypto.randomUUID,
// localStorage) and the bindings store owns all persistence.

import (
	"fmt"
	"net/http"
)

// sseFrame is one SSE event queued to a subscriber: an optional event
// NAME (the W1.4 delta/tool/usage family — named events are invisible
// to the MVP page's onmessage by SSE design) plus the data-JSON. The
// MVP reply keeps name empty — a default "message" event — so the
// pre-W1.4 wire shape is preserved byte-for-byte.
type sseFrame struct {
	name string // "" = default (message) event
	data string // the data: payload JSON
}

// convHeader carries the browser-minted conversation uuid (D-W3).
const convHeader = "X-Seek-Conversation"

// convQueryParam mirrors convHeader for GET /events: EventSource
// cannot set request headers.
const convQueryParam = "conv"

// convNativeMax bounds a conversation native id. A UUIDv4 is 36 chars;
// the slack keeps tests and future client formats legal.
const convNativeMax = 64

// convFromRequest resolves which conversation a request addresses:
// X-Seek-Conversation first, then ?conv=. Neither present → the legacy
// default conv web:default. A present-but-invalid value is an error the
// handlers answer 400 — silently rerouting it to the default conv
// would cross-wire two conversations (isolation is the point of W1.3).
func (f *WebFrontend) convFromRequest(r *http.Request) (ConvID, error) {
	v := r.Header.Get(convHeader)
	if v == "" {
		v = r.URL.Query().Get(convQueryParam)
	}
	if v == "" {
		return webConvID(), nil
	}
	if !validConvNative(v) {
		return "", fmt.Errorf("invalid %s value %q: 1–64 chars of [A-Za-z0-9._-] (crypto.randomUUID shape)", convHeader, truncateConv(v))
	}
	return NewConvID("web", v), nil
}

// validConvNative accepts the id charset the hub and bindings keys can
// carry without surprises in logs or JSON: alphanumerics plus -._ (the
// uuid charset and a little slack). No whitespace, no separators — a
// conv id is opaque, never prose.
func validConvNative(s string) bool {
	if s == "" || len(s) > convNativeMax {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}

// truncateConv keeps a garbage id short in error bodies.
func truncateConv(s string) string {
	if len(s) <= 32 {
		return s
	}
	return s[:32] + "…"
}

// subscribe registers one SSE client under conv and returns its event
// channel plus the unregister func. The conv's map entry leaves with
// its last subscriber, so conversations do not accumulate in a serve
// process that outlives many browser sessions.
func (f *WebFrontend) subscribe(conv ConvID) (chan sseFrame, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[ConvID]map[int]chan sseFrame)
	}
	if f.subs[conv] == nil {
		f.subs[conv] = make(map[int]chan sseFrame)
	}
	f.next++
	ch := make(chan sseFrame, 16)
	id := f.next
	f.subs[conv][id] = ch
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs[conv], id)
		if len(f.subs[conv]) == 0 {
			delete(f.subs, conv)
		}
	}
}

// broadcast fans one event out to every subscriber of conv — and only
// conv (W1.3 isolation). Best-effort: a full buffer drops the event
// for that client, a conv with no subscribers is a silent no-op (the
// documented MVP posture; the executor is never blocked).
func (f *WebFrontend) broadcast(conv ConvID, fr sseFrame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs[conv] {
		select {
		case ch <- fr:
		default: // slow client: drop, never block the executor
		}
	}
}
