package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// temperature_test.go pins the --temperature knob's two contracts (card
// c_trrsn7, judge precondition ①: the flag exists so a batch caller can
// pin 0 instead of paying for default sampling):
//
//  1. nil = NO temperature key on the wire, on BOTH lanes. Anything else
//     would change the request bytes for every existing user and bust
//     the prefix cache — the whole prompt prefix is re-billed at the miss
//     rate (~10×) on the first turn after such a drift.
//  2. a non-nil pointer rides every request verbatim, including an
//     explicit 0 (the value that matters most, and the one a `float64`
//     field would silently collapse into "absent" via omitempty).

// capturedBodies collects every request body the fake upstream saw.
func captureWire(t *testing.T, onBody func(raw []byte)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		onBody(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAgent_TemperatureDeepSeekWire covers the first-tier lane: the key
// is present exactly when the config sets it.
func TestAgent_TemperatureDeepSeekWire(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		temperature *float64
		// wantPresent is false when the key must be ABSENT from the body
		// (a present-but-null key would still move the bytes).
		wantPresent bool
		wantValue   float64
	}{
		{"unset → no key", nil, false, 0},
		{"explicit 0 → key with 0", ptrTo(0), true, 0},
		{"0.5 → key with 0.5", ptrTo(0.5), true, 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var raw []byte
			srv := captureWire(t, func(b []byte) {
				mu.Lock()
				raw = b
				mu.Unlock()
			})
			ag, err := New(Config{
				Client:      deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
				Model:       deepseek.ModelV41Flash,
				Temperature: c.temperature,
			})
			if err != nil {
				t.Fatal(err)
			}
			for range ag.Prompt(context.Background(), "hi") {
			}

			mu.Lock()
			body := append([]byte(nil), raw...)
			mu.Unlock()
			var decoded struct {
				Temperature *float64 `json:"temperature"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("decode request: %v\n%s", err, body)
			}
			// Presence is asserted on the RAW bytes, not the decoded
			// pointer: `"temperature":null` would decode to nil and
			// silently pass the pointer check while still breaking
			// byte-identity.
			hasKey := strings.Contains(string(body), `"temperature"`)
			if hasKey != c.wantPresent {
				t.Errorf("temperature key present = %v, want %v\n%s", hasKey, c.wantPresent, body)
			}
			if c.wantPresent && (decoded.Temperature == nil || *decoded.Temperature != c.wantValue) {
				t.Errorf("temperature = %v, want %v\n%s", decoded.Temperature, c.wantValue, body)
			}
		})
	}
}

// TestAgent_TemperatureSecondTierWire is the sibling for llm.Provider
// lanes: the agent forwards the same pointer, so an Anthropic / OpenAI /
// Gemini run behaves identically. The fake provider records the request
// struct directly — the byte-level assertion belongs to each adapter's
// own suite.
func TestAgent_TemperatureSecondTierWire(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		set      *float64
		wantNil  bool
		wantFrom float64
	}{
		{"unset → nil forwarded", nil, true, 0},
		{"0 → pointer forwarded", ptrTo(0), false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &recordingProvider{}
			ag, err := New(Config{
				Provider:    prov,
				Model:       "gpt-4o",
				Temperature: tc.set,
			})
			if err != nil {
				t.Fatal(err)
			}
			for range ag.Prompt(context.Background(), "hi") {
			}
			req := prov.last(t)
			if tc.wantNil {
				if req.Temperature != nil {
					t.Fatalf("Temperature = %v, want nil (absent key)", *req.Temperature)
				}
				return
			}
			if req.Temperature == nil {
				t.Fatal("Temperature dropped on the second-tier path — the knob is session-wide, not lane-specific")
			}
			if *req.Temperature != tc.wantFrom {
				t.Errorf("Temperature = %v, want %v", *req.Temperature, tc.wantFrom)
			}
		})
	}
}

func ptrTo(f float64) *float64 { return &f }

// Compile-time guard that the second-tier request struct still carries
// the field this file asserts on (a rename would otherwise fail confusingly
// inside a subtest).
var _ = llm.ChatRequest{}.Temperature
