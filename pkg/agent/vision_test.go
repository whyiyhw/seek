package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// TestPrompt_Images_ResolvedOnWire verifies the feature-vision send
// path end to end: Prompt(..., images) attaches an Asset-only part to
// the user message, ImageLoader materialises it into a data URL at
// send time, and the request body carries array-form content — while
// the agent's own history keeps the durable Asset reference (what a
// session save would persist).
func TestPrompt_Images_ResolvedOnWire(t *testing.T) {
	t.Parallel()
	var reqBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"a 3x2 chart"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
		ImageLoader: func(asset string) (string, error) {
			return "data:image/png;base64," + asset, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ag.Prompt(context.Background(), "what is this",
		deepseek.ImagePart{Asset: "ab12cd34ef56.png"}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}

	// Wire body: array-form content on the user message.
	var body struct {
		Messages []struct {
			Role    string           `json:"role"`
			Content json.RawMessage  `json:"content"`
			Images  *json.RawMessage `json:"images"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqBody, &body); err != nil {
		t.Fatal(err)
	}
	var user *struct {
		Role    string           `json:"role"`
		Content json.RawMessage  `json:"content"`
		Images  *json.RawMessage `json:"images"`
	}
	for i := range body.Messages {
		if body.Messages[i].Role == deepseek.RoleUser {
			user = &body.Messages[i]
		}
	}
	if user == nil {
		t.Fatal("no user message on wire")
	}
	var parts []map[string]any
	if err := json.Unmarshal(user.Content, &parts); err != nil {
		t.Fatalf("user content not array-form: %v (%s)", err, user.Content)
	}
	if len(parts) != 2 || parts[0]["type"] != "text" {
		t.Fatalf("parts = %v", parts)
	}
	iu, _ := parts[1]["image_url"].(map[string]any)
	if parts[1]["type"] != "image_url" || iu["url"] != "data:image/png;base64,ab12cd34ef56.png" {
		t.Fatalf("image part = %v", parts[1])
	}
	// The persistence-shape sibling must never cross the API.
	if user.Images != nil {
		t.Errorf("wire leaked images sibling: %s", *user.Images)
	}

	// In-memory history: durable Asset form, no data URL (that's the
	// request copy's business).
	for _, m := range ag.Messages() {
		if m.Role != deepseek.RoleUser {
			continue
		}
		if len(m.Images) != 1 || m.Images[0].Asset != "ab12cd34ef56.png" || m.Images[0].URL != "" {
			t.Fatalf("history must keep Asset-only form: %+v", m.Images)
		}
	}
}

// TestPrompt_MissingAsset_DegradesInBand: an unresolvable asset never
// kills the turn — the request still goes out, image-less, with an
// in-band note in the user content.
func TestPrompt_MissingAsset_DegradesInBand(t *testing.T) {
	t.Parallel()
	var reqBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
		ImageLoader: func(asset string) (string, error) {
			return "", errors.New("asset gone")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ag.Prompt(context.Background(), "look",
		deepseek.ImagePart{Asset: "deadbeef.png"}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("missing asset must degrade in-band, got error event: %v", e.Err)
		}
	}
	if !strings.Contains(string(reqBody), "[image: deadbeef.png") {
		t.Fatalf("in-band note missing from request: %s", reqBody)
	}
	if strings.Contains(string(reqBody), "image_url") {
		t.Fatalf("failed part must be dropped: %s", reqBody)
	}
}

// TestSummarise_StripsImages: the /compact side-channel call must not
// carry — or require resolving — image bytes.
func TestSummarise_StripsImages(t *testing.T) {
	t.Parallel()
	var reqBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"briefing"}}],"usage":{}}`)
	}))
	defer srv.Close()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Seed an image-bearing user message directly into history (as a
	// resume would).
	ag.Reset([]deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "look",
		Images:  []deepseek.ImagePart{{Asset: "ab12cd34ef56.png"}},
	}})
	if _, _, err := ag.Summarise(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reqBody), "images") || strings.Contains(string(reqBody), "image_url") {
		t.Fatalf("summarise leaked images: %s", reqBody)
	}
	// History itself is untouched by Summarise.
	for _, m := range ag.Messages() {
		if m.Role == deepseek.RoleUser && len(m.Images) == 1 {
			return // preserved — good
		}
	}
	t.Fatal("Summarise must not mutate history")
}

// TestPrompt_NonVisionModel_StripsHistoryImages: /model can switch a
// session to a non-vision model while image-bearing messages sit in
// history. The send path must drop them (markers stay in Content)
// rather than 400 the whole turn.
func TestPrompt_NonVisionModel_StripsHistoryImages(t *testing.T) {
	t.Parallel()
	var reqBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  "deepseek-v4-flash", // retired id = non-vision unknown, as if /model switched
		ImageLoader: func(asset string) (string, error) {
			return "data:image/png;base64," + asset, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ag.Reset([]deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "look\n\n[image: a.png — attached natively · 3x2 · 1 KiB]",
		Images:  []deepseek.ImagePart{{Asset: "a.png"}},
	}})
	for ev := range ag.Prompt(context.Background(), "and now?") {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("non-vision replay must not error: %v", e.Err)
		}
	}
	body := string(reqBody)
	if strings.Contains(body, "image_url") || strings.Contains(body, `"images"`) {
		t.Fatalf("images leaked to non-vision model: %s", body)
	}
	if !strings.Contains(body, "[image: a.png") {
		t.Fatalf("marker must stay for self-description: %s", body)
	}
}

// recordingProvider is a second-tier llm.Provider that records the
// request it was handed, so the send-path attachment decisions can be
// asserted without a wire server.
type recordingProvider struct {
	mu   sync.Mutex
	reqs []llm.ChatRequest
}

func (p *recordingProvider) ChatStream(_ context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	ch := make(chan llm.Event, 2)
	ch <- llm.TextDelta{Delta: "ok"}
	ch <- llm.TurnDone{FinishReason: "stop"}
	close(ch)
	return ch, nil
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) last(t *testing.T) llm.ChatRequest {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		t.Fatal("provider saw no request")
	}
	return p.reqs[len(p.reqs)-1]
}

// TestPrompt_LLMPath_CarriesImages: the second-tier send path must
// attach image bytes on a vision-capable model. This is the regression
// the OpenCode Go lane hit — its deepseek-v4.1-flash accepted images but
// the agent's llm path dropped every part (msgsToLLM was text-only) and
// the submit-time router had already refused to store them.
func TestPrompt_LLMPath_CarriesImages(t *testing.T) {
	prov := &recordingProvider{}
	ag, err := New(Config{
		Provider: prov,
		Model:    "deepseek-v4.1-flash", // OpenCode Go lane id, vision-capable
		ImageLoader: func(asset string) (string, error) {
			return "data:image/png;base64," + asset, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ag.Prompt(context.Background(), "what is this",
		deepseek.ImagePart{Asset: "ab12cd34ef56.png"}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	req := prov.last(t)
	if len(req.Messages) == 0 {
		t.Fatal("no messages")
	}
	var found bool
	for _, m := range req.Messages {
		if len(m.Images) == 1 && m.Images[0].URL == "data:image/png;base64,ab12cd34ef56.png" {
			found = true
		}
	}
	if !found {
		t.Fatalf("image part did not reach the provider: %+v", req.Messages)
	}
	// The agent's own history keeps the durable Asset reference.
	hist := ag.Messages()
	var ok bool
	for _, m := range hist {
		if len(m.Images) == 1 && m.Images[0].Asset == "ab12cd34ef56.png" && m.Images[0].URL == "" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("history must keep the Asset form: %+v", hist)
	}
}

// TestPrompt_LLMPath_NonVisionDropsImages: the same path on a model with
// no image capability must strip the parts and record the in-band note —
// never hand a 400-triggering payload to the provider.
func TestPrompt_LLMPath_NonVisionDropsImages(t *testing.T) {
	prov := &recordingProvider{}
	ag, err := New(Config{
		Provider:    prov,
		Model:       "glm-5.3", // no catalogue grant in tests → not vision
		ImageLoader: func(asset string) (string, error) { return "data:image/png;base64," + asset, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ag.Reset([]deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "look\n\n[image: a.png — attached natively · 3x2 · 1 KiB]",
		Images:  []deepseek.ImagePart{{Asset: "a.png"}},
	}})
	for ev := range ag.Prompt(context.Background(), "and now?") {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("non-vision replay must not error: %v", e.Err)
		}
	}
	req := prov.last(t)
	for _, m := range req.Messages {
		if len(m.Images) > 0 {
			t.Fatalf("images leaked to a non-vision model: %+v", m)
		}
	}
	// The reply must stay self-describing: the marker text is in history
	// already, so the model can still see an image was discussed.
	var marker bool
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "[image: a.png") {
			marker = true
		}
	}
	if !marker {
		t.Error("marker text must survive the strip")
	}
}

// TestPrompt_LLMPath_MissingAssetDegradesInBand: a vanished asset must
// surface as the in-band note on the second-tier path too (the DeepSeek
// path has the same guarantee via ResolveImages).
func TestPrompt_LLMPath_MissingAssetDegradesInBand(t *testing.T) {
	prov := &recordingProvider{}
	ag, err := New(Config{
		Provider:    prov,
		Model:       "deepseek-v4.1-flash",
		ImageLoader: func(string) (string, error) { return "", errors.New("asset gone") },
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ag.Prompt(context.Background(), "look",
		deepseek.ImagePart{Asset: "dead.png"}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("missing asset must not abort the turn: %v", e.Err)
		}
	}
	req := prov.last(t)
	var noted bool
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "[image: dead.png") && strings.Contains(m.Content, "asset gone") {
			noted = true
		}
		if len(m.Images) > 0 {
			t.Errorf("unresolvable part must be dropped: %+v", m.Images)
		}
	}
	if !noted {
		t.Fatalf("degradation note missing: %+v", req.Messages)
	}
}
