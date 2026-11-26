package llm

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestNewHTTPClient_HandshakeBudget pins the shared second-tier client:
// every OpenAI-compatible / Anthropic / Gemini adapter builds on it, so the
// handshake budget here is literally the budget all of those lanes get
// (card c_3j2ovf — the stdlib's 10s default killed whole rounds on a slow
// proxy tunnel).
func TestNewHTTPClient_HandshakeBudget(t *testing.T) {
	hc := NewHTTPClient()
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", hc.Transport)
	}
	if tr.TLSHandshakeTimeout != HandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, HandshakeTimeout)
	}
	if HandshakeTimeout <= 10*time.Second {
		t.Errorf("HandshakeTimeout = %v: must exceed the stdlib default (10s)", HandshakeTimeout)
	}
	if tr.Proxy == nil {
		t.Error("Proxy = nil: cloning DefaultTransport must keep proxy-from-environment")
	}
	if hc.Timeout <= 0 {
		t.Error("whole-request Timeout must stay set (a stream that never ends must not hang the turn)")
	}
}

// TestNewHTTPClient_SharesOneTransport: one transport per process, so the
// lanes share a connection pool (they all pooled through
// DefaultTransport before; this keeps that property).
func TestNewHTTPClient_SharesOneTransport(t *testing.T) {
	if NewHTTPClient().Transport != NewHTTPClient().Transport {
		t.Error("NewHTTPClient built two transports; lanes must share one pool")
	}
}

// TestNewHTTPClient_RealHandshakeDeadline: an injected short budget on the
// same transport shape really does fire against a peer that accepts TCP and
// never answers — i.e. TLSHandshakeTimeout is wired, not merely stored.
func TestNewHTTPClient_RealHandshakeDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the socket open without a ServerHello.
			go func(c net.Conn) {
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(conn)
		}
	}()

	hc := &http.Client{
		// No proxy: ProxyFromEnvironment would route through a developer's
		// proxy and never reach this listener.
		Transport: &http.Transport{TLSHandshakeTimeout: 60 * time.Millisecond},
		Timeout:   2 * time.Second,
	}
	url := "https://" + ln.Addr().String() + "/"
	if _, err := hc.Get(url); err == nil {
		t.Fatal("expected the handshake deadline to fire")
	}

	// The budget change must not weaken TLS: the shared transport keeps the
	// system roots and no InsecureSkipVerify.
	if live, ok := NewHTTPClient().Transport.(*http.Transport); ok {
		if live.TLSClientConfig != nil && live.TLSClientConfig.InsecureSkipVerify {
			t.Error("TLSClientConfig.InsecureSkipVerify set: the budget change must not weaken TLS")
		}
	}
}
