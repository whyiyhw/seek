package deepseek

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
)

// --- handshake budget + timeout-class backoff (card c_3j2ovf) ------------
//
// Mirror of pkg/llm/provider/openai's transport_test.go on the first-tier
// lane: the retry shapes of the two packages must stay identical (AGENTS.md
// layer rule — pkg/deepseek may not import pkg/llm, so the rule is one-by-
// hand rather than shared).

// TestNew_HandshakeBudget pins the transport New hands out: 20s handshake
// budget with proxy-from-environment preserved.
func TestNew_HandshakeBudget(t *testing.T) {
	c := New(WithAPIKey("k"))
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.http.Transport)
	}
	if got := tr.TLSHandshakeTimeout; got != handshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", got, handshakeTimeout)
	}
	if handshakeTimeout <= 10*time.Second {
		t.Errorf("handshakeTimeout = %v: must exceed the stdlib default (10s)", handshakeTimeout)
	}
	if tr.Proxy == nil {
		t.Error("Proxy = nil: cloning DefaultTransport must keep proxy-from-environment")
	}
}

// TestWithHTTPClient pins the injection seam: the option must replace the
// client wholesale, so a test's short handshake budget is what actually
// governs the wire.
func TestWithHTTPClient(t *testing.T) {
	hc := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSHandshakeTimeout: 42 * time.Millisecond}}
	c := New(WithAPIKey("k"), WithHTTPClient(hc))
	if c.http != hc {
		t.Fatalf("WithHTTPClient did not replace the client: %p != %p", c.http, hc)
	}
}

// TestBackoffFor_ClassTable pins the classification: only a net.Error whose
// Timeout() is true earns the long wait; resets, EOFs and API status errors
// keep the 500ms blip backoff.
func TestBackoffFor_ClassTable(t *testing.T) {
	timeoutErr := fmt.Errorf("deepseek: http: %w", &url.Error{
		Op:  "Post",
		URL: "https://example.test",
		Err: tlsHandshakeTimeoutLike{},
	})
	cases := []struct {
		name string
		err  error
		want time.Duration
	}{
		{"handshake timeout", timeoutErr, timeoutBackoff},
		{"context deadline", fmt.Errorf("deepseek: http: %w", context.DeadlineExceeded), timeoutBackoff},
		{"immediate EOF", errors.New("deepseek: http: EOF"), retryBackoff},
		{"status 500", &APIError{StatusCode: 500, Message: "boom"}, retryBackoff},
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
				_, _ = io.Copy(io.Discard, c)
				_ = c.Close()
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *stallTLSServer) URL() string { return "https://" + s.ln.Addr().String() }

// TestChatStream_HandshakeTimeout_LongBackoff is the card's end-to-end proof
// on the first-tier lane: the error really is the TLS handshake deadline,
// the lane retries once, and the second attempt waits the timeout-class
// backoff rather than 500ms.
func TestChatStream_HandshakeTimeout_LongBackoff(t *testing.T) {
	srv := newStallTLSServer(t)

	hc := &http.Client{
		Transport: &http.Transport{
			Proxy:               nil, // no env proxy: it would bypass our listener
			TLSHandshakeTimeout: 60 * time.Millisecond,
		},
		Timeout: 10 * time.Second,
	}
	c := New(WithAPIKey("t"), WithBaseURL(srv.URL()), WithHTTPClient(hc))

	_, err := c.ChatStream(context.Background(), &ChatRequest{
		Model:    ModelV41Flash,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
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
