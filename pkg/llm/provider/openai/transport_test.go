package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/llm"
)

// --- handshake budget (card c_3j2ovf) ------------------------------------
//
// The bug: a stalled TLS handshake surfaced as
//
//	openai: http: Post "…": net/http: TLS handshake timeout (after 2 attempts)
//
// and killed the whole round. Two halves of the fix are pinned here:
// the transport's handshake budget (20s, not the stdlib's 10s) and the
// timeout-class long backoff (a 500ms retry walks back into the same
// stall).

// TestNewClient_HandshakeBudget pins the transport every constructor hands
// out: 20s handshake budget, proxy-from-environment preserved.
func TestNewClient_HandshakeBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    *Client
	}{
		{"New", New("k")},
		{"NewCompatible", NewCompatible("k", "https://example.test/v1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, ok := tc.c.http.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("%s: transport is %T, want *http.Transport", tc.name, tc.c.http.Transport)
			}
			if got := tr.TLSHandshakeTimeout; got != llm.HandshakeTimeout {
				t.Errorf("TLSHandshakeTimeout = %v, want %v", got, llm.HandshakeTimeout)
			}
			if llm.HandshakeTimeout <= 10*time.Second {
				t.Errorf("llm.HandshakeTimeout = %v: must exceed the stdlib default (10s)", llm.HandshakeTimeout)
			}
			if tr.Proxy == nil {
				t.Error("Proxy = nil: cloning DefaultTransport must keep proxy-from-environment")
			}
		})
	}
}

// TestBackoffFor_ClassTable pins the classification: only a net.Error whose
// Timeout() is true earns the long wait; resets, EOFs and HTTP status errors
// keep the 500ms blip backoff.
func TestBackoffFor_ClassTable(t *testing.T) {
	timeoutErr := fmt.Errorf("openai: http: %w", &url.Error{
		Op:  "Post",
		URL: "https://example.test/v1",
		Err: tlsHandshakeTimeoutLike{},
	})
	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"handshake timeout", timeoutErr, timeoutBackoff},
		{"context deadline", fmt.Errorf("openai: http: %w", context.DeadlineExceeded), timeoutBackoff},
		{"immediate EOF", errors.New("openai: http: EOF"), retryBackoff},
		{"status 500", errors.New("openai: status 500: boom"), retryBackoff},
		{"nil", nil, retryBackoff},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := backoffFor(c.err); got != c.want {
				t.Errorf("backoffFor(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// tlsHandshakeTimeoutLike mimics net/http's unexported tlsHandshakeTimeoutError:
// a net.Error that reports Timeout() and Temporary() true.
type tlsHandshakeTimeoutLike struct{}

func (tlsHandshakeTimeoutLike) Error() string   { return "net/http: TLS handshake timeout" }
func (tlsHandshakeTimeoutLike) Timeout() bool   { return true }
func (tlsHandshakeTimeoutLike) Temporary() bool { return true }

// stallTLSServer accepts TCP connections and never completes a handshake —
// the shape a cold proxy tunnel or a wedged gateway presents to the client.
// Each accept's timestamp is recorded so a test can measure the retry gap.
type stallTLSServer struct {
	ln      net.Listener
	accepts chan time.Time
}

func newStallTLSServer(t *testing.T) *stallTLSServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &stallTLSServer{ln: ln, accepts: make(chan time.Time, 8)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepts <- time.Now()
			go func(c net.Conn) {
				// Hold the socket open without a ServerHello; the client's
				// handshake deadline fires and it closes its side, which
				// ends this copy.
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *stallTLSServer) URL() string {
	return "https://" + s.ln.Addr().String() + "/v1/chat/completions"
}

// TestChatStream_HandshakeTimeout_LongBackoff is the card's end-to-end proof:
// the error really is the TLS handshake deadline (not a test-only fake), the
// lane still retries once, and the second attempt happens after the timeout
// backoff rather than 500ms — which is what makes the observability
// classification meaningful on the wire.
func TestChatStream_HandshakeTimeout_LongBackoff(t *testing.T) {
	srv := newStallTLSServer(t)

	hc := &http.Client{
		Transport: &http.Transport{
			// No proxy: ProxyFromEnvironment would route through a
			// developer's HTTPS_PROXY and never reach our listener.
			Proxy:               nil,
			TLSHandshakeTimeout: 60 * time.Millisecond,
		},
		Timeout: 10 * time.Second,
	}
	c := newWithClient("k", srv.URL(), hc)

	_, err := c.ChatStream(context.Background(), llm.ChatRequest{})
	if err == nil {
		t.Fatal("expected an error once both handshakes time out")
	}
	if !strings.Contains(err.Error(), "TLS handshake timeout") {
		t.Errorf("error = %v, want the real transport TLS handshake timeout", err)
	}
	if !strings.Contains(err.Error(), "(after 2 attempts)") {
		t.Errorf("error = %v, want it annotated with the attempt count", err)
	}

	var first, second time.Time
	for i := 0; i < 2; i++ {
		select {
		case at := <-srv.accepts:
			if i == 0 {
				first = at
			} else {
				second = at
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("saw only %d connection attempt(s), want 2", i)
		}
	}
	if gap := second.Sub(first); gap < timeoutBackoff {
		t.Errorf("retry gap = %v, want >= %v (a timed-out attempt must not reuse the 500ms blip backoff)", gap, timeoutBackoff)
	}
}
