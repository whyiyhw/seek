package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// A cleared registry is the `-tools=none` surface: non-nil (so the agent
// still treats tools as configured) but empty. The DeepSeek request must
// then carry NO `tools` key at all — `Tools` is `omitempty`, and an empty
// slice is as absent as nil. Asserting on the raw body is the only honest
// check: a decoder would read "no tools array" and "empty tools array"
// identically, while the wire bytes are what the model (and the prefix
// cache) actually sees.
func TestAgent_ClearedRegistrySendsNoToolsField(t *testing.T) {
	t.Parallel()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	reg := tools.New().Add(&stubTool{name: "read", schema: `{"type":"object"}`, reply: "x"})
	reg.Clear()

	ag, err := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
		Tools:        reg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res := drainAgent(ag.Prompt(context.Background(), "judge this"))
	if len(res.errors) > 0 {
		t.Fatalf("unexpected error event: %v", res.errors[0])
	}
	if len(body) == 0 {
		t.Fatal("backend captured no request body")
	}
	if strings.Contains(string(body), `"tools"`) {
		t.Errorf("cleared registry still put a tools field on the wire:\n%s", body)
	}
}
