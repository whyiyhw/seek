package llm

import (
	"net/http"
	"sync"
	"time"
)

// HandshakeTimeout is the TLS-handshake budget every second-tier lane uses.
//
// The stdlib default (10s, net/http.DefaultTransport) is a general-purpose
// web-request number. seek's shape is different: a streaming POST through an
// HTTP proxy tunnel (HTTPS_PROXY / a local mihomo), where the handshake is
// the first thing that can be slow — a cold CONNECT plus a TLS negotiation
// over a lossy path regularly lands near 1s, and a transient proxy/gateway
// stall pushes it past 10s. A single slow handshake then reads as "the
// endpoint is down" and kills the round (card c_3j2ovf; the observed line
// was `net/http: TLS handshake timeout (after 2 attempts)` on the OpenDesign
// Cloud lane, with the same string already in the ledger twice for
// api.deepseek.com). 20s costs nothing when the network is healthy and buys
// the whole class of "slow but not down" handshakes.
//
// pkg/deepseek keeps its own identical constant: that package must not
// import pkg/llm (CI layer-lint), so the two lanes agree by hand, not by
// sharing a symbol. Keep the values equal.
const HandshakeTimeout = 20 * time.Second

// httpClientTimeout is the whole-request cap. It has to clear a long
// streamed answer, so it is deliberately generous; the handshake budget
// above is the part that must be tighter than the stdlib.
const httpClientTimeout = 5 * time.Minute

// transport is the process-wide transport for every second-tier lane,
// cloned from http.DefaultTransport so proxy-from-environment, HTTP/2
// attempt and idle-connection tuning stay exactly the stdlib's (only the
// handshake budget differs). One instance shared by all adapters means one
// connection pool — which is what they had implicitly before, since a
// client with no Transport falls back to DefaultTransport anyway.
var transport = sync.OnceValue(func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.TLSHandshakeTimeout = HandshakeTimeout
	return t
})

// NewHTTPClient returns the *http.Client every second-tier provider adapter
// should use: a 5-minute request cap over the shared transport (see
// transport) with the raised handshake budget. Tests that need a shorter
// handshake deadline construct their own client instead — that is the seam
// each adapter exposes.
func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: httpClientTimeout, Transport: transport()}
}
