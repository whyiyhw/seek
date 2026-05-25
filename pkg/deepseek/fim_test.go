package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
