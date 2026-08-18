package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFIM_HappyPath(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != endpointFIM {
			t.Errorf("path = %s, want %s", r.URL.Path, endpointFIM)
		}
		// Capture the body to verify Prompt+Suffix made it through.
		var body FIMRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Prompt != "func add(a, b int) int {\n  " || body.Suffix != "\n}" {
			t.Errorf("unexpected body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"x","object":"text_completion",
			"choices":[{"index":0,"text":"return a + b","finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":12}
		}`)
	}))
	defer srv.Close()

	c := New(WithAPIKey("test"), WithBaseURL(srv.URL))
	resp, err := c.FIM(context.Background(), &FIMRequest{
		Prompt: "func add(a, b int) int {\n  ",
		Suffix: "\n}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Choices[0].Text; got != "return a + b" {
		t.Errorf("text = %q", got)
	}
	if resp.Usage.PromptTokens != 12 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestFIM_RequiresPrompt(t *testing.T) {
	t.Parallel()
	c := New(WithAPIKey("t"))
	_, err := c.FIM(context.Background(), &FIMRequest{})
	if err == nil || !strings.Contains(err.Error(), "non-empty Prompt") {
		t.Errorf("err = %v", err)
	}
}

func TestFIM_APIErrorPropagates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request","message":"prompt too long"}}`)
	}))
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.FIM(context.Background(), &FIMRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *APIError
	if !errorAs(err, &ae) {
		t.Fatalf("err = %v, want APIError", err)
	}
	if ae.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", ae.StatusCode)
	}
}

// TestFIM_RetryOnTransportEOF covers the FIM path's transport-error
// retry: the first connection is closed before any response (EOF), the
// retry succeeds. Without retryCall this used to surface as a hard
// failure on the first attempt.
func TestFIM_RetryOnTransportEOF(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n == 1 {
			// Hijack and close the raw connection without writing any
			// response — the client observes a bare "EOF" from http.Do.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"x","object":"text_completion",
			"choices":[{"index":0,"text":"return a + b","finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":12}
		}`)
	}))
	defer srv.Close()

	c := New(WithAPIKey("test"), WithBaseURL(srv.URL))
	resp, err := c.FIM(context.Background(), &FIMRequest{Prompt: "func add(a, b int) int {\n  ", Suffix: "\n}"})
	if err != nil {
		t.Fatalf("FIM: %v", err)
	}
	if got := resp.Choices[0].Text; got != "return a + b" {
		t.Errorf("text = %q", got)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hits = %d, want 2 (first=EOF, second=200)", got)
	}
}

// TestFIM_RetryExhaustedAnnotated pins the FIM exhaustion path: every
// attempt fails, the error keeps the API cause and is annotated with the
// attempt count.
func TestFIM_RetryExhaustedAnnotated(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"type":"internal_error","message":"fim boom"}}`)
	}))
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.FIM(context.Background(), &FIMRequest{Prompt: "x"})
	if err == nil {
		t.Fatalf("expected error after retry exhausted")
	}
	if !strings.Contains(err.Error(), "fim boom") {
		t.Errorf("error should preserve original message, got %v", err)
	}
	if !strings.Contains(err.Error(), "(after 2 attempts)") {
		t.Errorf("error should be annotated with the attempt count, got %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server hits = %d, want exactly 2 (initial + one retry)", got)
	}
}

// errorAs is a tiny shim so the file doesn't need errors.As'd type
// imports just for one assertion.
func errorAs(err error, target any) bool {
	type asable interface{ As(any) bool }
	if _, ok := err.(asable); ok {
		return err.(asable).As(target)
	}
	// fall back to errors.As semantics
	for e := err; e != nil; {
		if ae, ok := e.(*APIError); ok {
			if t, ok := target.(**APIError); ok {
				*t = ae
				return true
			}
		}
		break
	}
	return false
}
