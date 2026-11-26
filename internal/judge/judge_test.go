package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ---- input contract -------------------------------------------------

func TestParseEntries_Table(t *testing.T) {
	const good = `{"id":"a1","text":"hello","meta":{"source":"hn"}}`
	cases := []struct {
		name     string
		in       string
		wantLen  int
		wantErr  string // substring; "" means the entry must be usable
		wantID   string
		wantMeta string
	}{
		{
			name:     "valid with meta",
			in:       good,
			wantLen:  1,
			wantID:   "a1",
			wantMeta: `{"source":"hn"}`,
		},
		{
			name:    "valid without meta",
			in:      `{"id":"b2","text":"x"}`,
			wantLen: 1,
			wantID:  "b2",
		},
		{
			name:    "invalid json",
			in:      `{"id":"a1","text":`,
			wantLen: 1,
			wantErr: "invalid JSON line",
		},
		{
			name:    "missing id",
			in:      `{"text":"orphan"}`,
			wantLen: 1,
			wantErr: "missing id",
		},
		{
			name:    "empty id",
			in:      `{"id":"   ","text":"x"}`,
			wantLen: 1,
			wantErr: "missing id",
		},
		{
			name:    "empty text",
			in:      `{"id":"a3","text":"  "}`,
			wantLen: 1,
			wantID:  "a3",
			wantErr: "empty text",
		},
		{
			name:    "blank lines skipped",
			in:      "\n" + good + "\n\n" + `{"id":"b2","text":"y"}` + "\n",
			wantLen: 2,
		},
		{
			name:    "BOM tolerated",
			in:      "\ufeff" + good,
			wantLen: 1,
			wantID:  "a1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseEntries([]byte(c.in))
			if len(got) != c.wantLen {
				t.Fatalf("entries = %d, want %d (%+v)", len(got), c.wantLen, got)
			}
			if c.wantLen != 1 {
				return
			}
			e := got[0]
			if c.wantErr == "" {
				if e.Err != "" {
					t.Fatalf("unexpected line error: %s", e.Err)
				}
			} else if !strings.Contains(e.Err, c.wantErr) {
				t.Fatalf("error = %q, want substring %q", e.Err, c.wantErr)
			}
			if c.wantID != "" && e.Item.ID != c.wantID {
				t.Errorf("id = %q, want %q", e.Item.ID, c.wantID)
			}
			if c.wantMeta != "" && string(e.Item.Meta) != c.wantMeta {
				t.Errorf("meta = %s, want %s (verbatim passthrough)", e.Item.Meta, c.wantMeta)
			}
		})
	}
}

// TestParseEntries_MetaIsOpaque: meta is never interpreted, so an
// arbitrary nested shape round-trips byte-for-byte.
func TestParseEntries_MetaIsOpaque(t *testing.T) {
	in := `{"id":"a1","text":"t","meta":{"deep":{"n":1,"list":[1,2]}}}`
	got := ParseEntries([]byte(in))
	if len(got) != 1 || got[0].Err != "" {
		t.Fatalf("entries = %+v", got)
	}
	if string(got[0].Item.Meta) != `{"deep":{"n":1,"list":[1,2]}}` {
		t.Fatalf("meta = %s", got[0].Item.Meta)
	}
}

// TestParseEntry_SingleLine pins the streaming entry point: a caller
// feeding lines from its own reader gets the same verdict as ParseEntries.
func TestParseEntry_SingleLine(t *testing.T) {
	got := ParseEntry([]byte(`{"id":"a1","text":"x","meta":{"k":1}}`))
	if got.Err != "" || got.Item.ID != "a1" || string(got.Item.Meta) != `{"k":1}` {
		t.Fatalf("entry = %+v", got)
	}
	if bad := ParseEntry([]byte(`garbage`)); bad.Err == "" {
		t.Fatalf("garbage line must carry an error, got %+v", bad)
	}
}

// TestModel_EchoesWireID: _meta's model field comes from this accessor,
// so a caller echoing the id it configured gets it back verbatim.
func TestModel_EchoesWireID(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	j, err := New(fb.client(), "custom-lane", "r")
	if err != nil {
		t.Fatal(err)
	}
	if got := j.Model(); got != "custom-lane" {
		t.Fatalf("Model() = %q, want custom-lane", got)
	}
	// Default model id when the caller passes "".
	j2, err := New(fb.client(), "", "r")
	if err != nil {
		t.Fatal(err)
	}
	if got := j2.Model(); got != deepseek.ModelV41Flash {
		t.Fatalf("Model() = %q, want %q", got, deepseek.ModelV41Flash)
	}
}

// TestExtractObject_Table covers the brace scanner: no object, balanced
// object, braces inside strings, and an unclosed object.
func TestExtractObject_Table(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"no braces at all", "", false},
		{"prefix {} suffix", "{}", true},
		{`{"a":"}"}`, `{"a":"}"}`, true},
		{`{"a":"{nested}"}`, `{"a":"{nested}"}`, true},
		{"{unclosed", "", false},
		// A backslash-escaped quote inside the string must not end the
		// string: the scanner has to honour JSON escaping.
		{`{"a":"say \"hi}"}`, `{"a":"say \"hi}"}`, true},
	}
	for _, c := range cases {
		got, ok := extractObject(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("extractObject(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestExcerpt_BoundsLongReplies: a runaway reply must not be pasted
// whole into the caller's error row.
func TestExcerpt_BoundsLongReplies(t *testing.T) {
	short := excerpt("  one   two  ")
	if short != `"one two"` {
		t.Fatalf("short excerpt = %s", short)
	}
	long := strings.Repeat("boom ", 200)
	got := excerpt(long)
	if len([]rune(got)) > 100 {
		t.Fatalf("long excerpt is %d runes, want ≤ 100: %s", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated excerpt must end with an ellipsis marker, got %s", got)
	}
}

// ---- reply parsing --------------------------------------------------

func TestParseReply_Table(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string // verdict, "" means error expected
		wantErr string
	}{
		{
			name: "bare object",
			in:   `{"verdict":"keep","reasons":["rfc cited"],"signals":{"relevant":"yes"}}`,
			want: VerdictKeep,
		},
		{
			name: "fenced and prose-wrapped",
			in:   "Here you go:\n```json\n{\"verdict\":\"drop\",\"reasons\":[\"marketing\"]}\n```\nDone.",
			want: VerdictDrop,
		},
		{
			name: "uppercase and spaced verdict",
			in:   `{"verdict":"  GRAY ","reasons":["unsure"]}`,
			want: VerdictGray,
		},
		{
			name: "brace inside a reason",
			in:   `{"verdict":"keep","reasons":["mentions {curly} braces"]}`,
			want: VerdictKeep,
		},
		{
			name: "verdict case-normalised from token",
			in:   `{"verdict":"Keep","reasons":["x"]}`,
			want: VerdictKeep,
		},
		{
			name:    "invalid verdict degrades to error",
			in:      `{"verdict":"maybe","reasons":["looks fine"]}`,
			wantErr: "malformed verdict",
		},
		{
			name:    "no reasons",
			in:      `{"verdict":"keep","reasons":[]}`,
			wantErr: "no reasons",
		},
		{
			name:    "blank reasons",
			in:      `{"verdict":"drop","reasons":["  ",""]}`,
			wantErr: "no reasons",
		},
		{
			name:    "prose only",
			in:      "I think this one is probably a keep.",
			wantErr: "malformed verdict",
		},
		{
			name:    "empty content",
			in:      "   ",
			wantErr: "empty content",
		},
		{
			name:    "truncated json with verdict still visible",
			in:      `{"verdict":"keep","reasons":["a",`,
			wantErr: "no decodable reasons",
		},
		{
			name:    "loose unquoted verdict form",
			in:      "verdict: drop\nbecause it is marketing",
			wantErr: "no decodable reasons",
		},
		{
			name: "unclosed object (brace eaten by the fence)",
			in:   "```\n{\"verdict\":\"keep\",\"reasons\":[\"ok\"]\n```",
			// The object never closes → extractObject fails; the quoted
			// verdict is still visible, but reasons are not decodable.
			wantErr: "no decodable reasons",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := parseReply(c.in)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error, got %+v", r)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %q, want substring %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.Verdict != c.want {
				t.Fatalf("verdict = %q, want %q", r.Verdict, c.want)
			}
			if len(r.Reasons) == 0 {
				t.Fatalf("reasons must be non-empty on success")
			}
		})
	}
}

// ---- fake DeepSeek backend ------------------------------------------

// fakeBackend serves canned replies keyed by the entry text found in the
// user message, so a batch test can make one entry behave badly while
// the rest succeed. It is httptest-backed: no real key, no network.
//
// It also records every request so tests can assert on the WIRE shape —
// the judge kernel's contract lives in the bytes it sends.
type fakeBackend struct {
	mu        sync.Mutex
	replies   map[string]string // text → raw model content
	failText  string            // text whose call returns HTTP 401
	requests  []wireRequest
	calls     int32
	usage     deepseek.Usage
	srv       *httptest.Server
	modelEcho string
}

type wireRequest struct {
	Model           string                 `json:"model"`
	Messages        []deepseek.Message     `json:"messages"`
	Temperature     *float64               `json:"temperature"`
	MaxTokens       int                    `json:"max_tokens"`
	Tools           json.RawMessage        `json:"tools"`
	ResponseFormat  json.RawMessage        `json:"response_format"`
	ReasoningEffort string                 `json:"reasoning_effort"`
	Thinking        *deepseek.ThinkingMode `json:"thinking"`
}

func newFakeBackend(t *testing.T, replies map[string]string, failText string) *fakeBackend {
	t.Helper()
	fb := &fakeBackend{
		replies:  replies,
		failText: failText,
		usage: deepseek.Usage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
			PromptCacheHitTokens: 90, PromptCacheMissTokens: 10,
		},
		modelEcho: "fixture-model",
	}
	fb.srv = httptest.NewServer(http.HandlerFunc(fb.handle))
	t.Cleanup(fb.srv.Close)
	return fb
}

func (fb *fakeBackend) handlerURL() string { return fb.srv.URL }

func (fb *fakeBackend) handle(w http.ResponseWriter, r *http.Request) {
	body := wireRequest{}
	if err := decodeBody(r, &body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"bad body: %s"}}`, err)
		return
	}
	fb.mu.Lock()
	fb.requests = append(fb.requests, body)
	fb.calls++
	fb.mu.Unlock()

	text := userText(body.Messages)
	if fb.failText != "" && strings.Contains(text, fb.failText) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails"}}`))
		return
	}
	content, ok := fb.replies[text]
	if !ok {
		content = `{"verdict":"gray","reasons":["no canned reply for this entry"]}`
	}
	resp := map[string]any{
		"id": "c1", "object": "chat.completion", "created": 1, "model": fb.modelEcho,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":            fb.usage.PromptTokens,
			"completion_tokens":        fb.usage.CompletionTokens,
			"total_tokens":             fb.usage.TotalTokens,
			"prompt_cache_hit_tokens":  fb.usage.PromptCacheHitTokens,
			"prompt_cache_miss_tokens": fb.usage.PromptCacheMissTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (fb *fakeBackend) snapshot() []wireRequest {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	out := make([]wireRequest, len(fb.requests))
	copy(out, fb.requests)
	return out
}

func decodeBody(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func userText(msgs []deepseek.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == deepseek.RoleUser {
			return strings.TrimPrefix(msgs[i].Content, entryHeader)
		}
	}
	return ""
}

func (fb *fakeBackend) client() *deepseek.Client {
	return deepseek.New(deepseek.WithAPIKey("sk-test"), deepseek.WithBaseURL(fb.srv.URL))
}

const testRubric = "KEEP if it cites a specific primary source.\nDROP if it is marketing.\nOtherwise GRAY."

// ---- batch behaviour ------------------------------------------------

func TestBatch_HappyPath_MetaAndOrder(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"cites RFC 9110":  `{"verdict":"keep","reasons":["cites a primary source"],"signals":{"relevant":"yes"}}`,
		"buy our product": `{"verdict":"drop","reasons":["purely promotional"]}`,
		"maybe a repost":  `{"verdict":"gray","reasons":["cannot tell if it is a repost"]}`,
	}, "")

	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}

	entries := ParseEntries([]byte(strings.Join([]string{
		`{"id":"a1","text":"cites RFC 9110","meta":{"source":"hn"}}`,
		`{"id":"a2","text":"buy our product"}`,
		`{"id":"a3","text":"maybe a repost","meta":{"source":"rss"}}`,
	}, "\n")))

	res := j.Batch(context.Background(), entries)
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(res.Rows))
	}
	wants := []struct{ id, verdict string }{
		{"a1", VerdictKeep}, {"a2", VerdictDrop}, {"a3", VerdictGray},
	}
	for i, w := range wants {
		if res.Rows[i].ID != w.id || res.Rows[i].Verdict != w.verdict {
			t.Fatalf("row %d = %+v, want id=%s verdict=%s", i, res.Rows[i], w.id, w.verdict)
		}
	}
	if string(res.Rows[0].Meta) != `{"source":"hn"}` {
		t.Errorf("row 0 meta = %s, want passthrough", res.Rows[0].Meta)
	}
	if res.Rows[1].Meta != nil {
		t.Errorf("row 1 must omit meta (absent on input), got %s", res.Rows[1].Meta)
	}

	m := res.Meta
	if m.Items != 3 || m.Kept != 1 || m.Dropped != 1 || m.Gray != 1 {
		t.Fatalf("meta counts = %+v, want items=3 keep/drop/gray=1/1/1", m)
	}
	if m.Model != deepseek.ModelV41Flash {
		t.Errorf("meta model = %q, want default %q", m.Model, deepseek.ModelV41Flash)
	}
	if m.RubricSHA256 != sha256Hex(testRubric) || len(m.RubricSHA256) != 64 {
		t.Errorf("rubric sha = %q", m.RubricSHA256)
	}
	if m.PromptTokens != 300 || m.CacheHitTokens != 270 || m.OutputTokens != 60 {
		t.Errorf("token aggregate = %+v, want 300/270/60 (3 calls × 100/90/20)", m)
	}
	if m.Temperature != nil {
		t.Errorf("temperature must stay nil when not set (no wire key, no _meta key): %v", *m.Temperature)
	}
	if m.Effort != "" {
		t.Errorf("effort must stay empty when not set, got %q", m.Effort)
	}

	// Cost reconciliation against the pricing table the meta claims.
	wantCost := 3 * pricing.Cost(deepseek.ModelV41Flash, pricing.TierStandard, fb.usage)
	if diff := m.CostUSD - wantCost; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost = %v, want %v (3 × per-call)", m.CostUSD, wantCost)
	}
}

// TestBatch_WireShape pins the request bytes: exactly two messages
// (system = preamble+rubric, user = entry), no tools field, no
// response_format, no temperature key unless pinned.
func TestBatch_WireShape(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"hello": `{"verdict":"keep","reasons":["ok"]}`,
	}, "")
	j, err := New(fb.client(), "", testRubric) // "" → the judge lane's default model
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte(`{"id":"a1","text":"hello"}`)))
	if res.Errors() != 0 {
		t.Fatalf("rows = %+v", res.Rows)
	}

	reqs := fb.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("calls = %d, want exactly 1 (one Chat per entry, no agent loop)", len(reqs))
	}
	req := reqs[0]
	if req.Model != deepseek.ModelV41Flash {
		t.Errorf("model = %q, want default %q", req.Model, deepseek.ModelV41Flash)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (system + user)", len(req.Messages))
	}
	if req.Messages[0].Role != deepseek.RoleSystem || req.Messages[1].Role != deepseek.RoleUser {
		t.Fatalf("roles = %s/%s", req.Messages[0].Role, req.Messages[1].Role)
	}
	if !strings.HasPrefix(req.Messages[0].Content, systemPreamble) || !strings.HasSuffix(req.Messages[0].Content, testRubric) {
		t.Fatalf("system message must be preamble + rubric verbatim")
	}
	if !strings.Contains(req.Messages[1].Content, "hello") {
		t.Fatalf("user message must carry the entry text, got %q", req.Messages[1].Content)
	}
	if len(req.Tools) != 0 {
		t.Errorf("tools must be absent from the request, got %s", req.Tools)
	}
	if len(req.ResponseFormat) != 0 {
		t.Errorf("response_format must stay unwired (PRD §4 D4), got %s", req.ResponseFormat)
	}
	if req.Temperature != nil {
		t.Errorf("temperature key must be absent when unpinned, got %v", *req.Temperature)
	}
	if req.MaxTokens != defaultMaxTokens {
		t.Errorf("max_tokens = %d, want default %d", req.MaxTokens, defaultMaxTokens)
	}
	// V4.1-Flash opts into thinking explicitly, mirroring pkg/agent.
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		t.Errorf("thinking = %+v, want enabled for %s", req.Thinking, deepseek.ModelV41Flash)
	}
	if req.ReasoningEffort != "" {
		t.Errorf("reasoning_effort must be absent when not configured, got %q", req.ReasoningEffort)
	}
}

// TestBatch_ReplyIDNeverOverridesCallerID is the "model invented an id"
// half of the malformed-reply test bar: the reply is never asked for an
// id, and a stray one is dropped — the row's id always comes from the
// caller's input line (invariant 1: id, not position or model output).
func TestBatch_ReplyIDNeverOverridesCallerID(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"x": `{"id":"invented-by-the-model","verdict":"keep","reasons":["ok"]}`,
	}, "")
	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte(`{"id":"caller-a1","text":"x"}`)))
	if res.Rows[0].ID != "caller-a1" {
		t.Fatalf("row id = %q, want the caller's id (a model-supplied id must never win)", res.Rows[0].ID)
	}
	if res.Rows[0].Verdict != VerdictKeep {
		t.Fatalf("row = %+v", res.Rows[0])
	}
}

// TestBatch_SystemBytesIdenticalAcrossEntries is the prefix-cache
// invariant: every entry of a batch sends byte-identical system bytes,
// so the rubric prefix hits cache (PRD §10).
func TestBatch_SystemBytesIdenticalAcrossEntries(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}
	j.Batch(context.Background(), ParseEntries([]byte(strings.Join([]string{
		`{"id":"a1","text":"entry-alpha"}`,
		`{"id":"a2","text":"entry-beta"}`,
		`{"id":"a3","text":"entry-gamma"}`,
	}, "\n"))))

	reqs := fb.snapshot()
	if len(reqs) != 3 {
		t.Fatalf("calls = %d, want 3", len(reqs))
	}
	want := reqs[0].Messages[0].Content
	for i, r := range reqs[1:] {
		if r.Messages[0].Content != want {
			t.Fatalf("entry %d system bytes differ — prefix cache would miss", i+1)
		}
	}
	for i, r := range reqs {
		if strings.Contains(r.Messages[0].Content, []string{"entry-alpha", "entry-beta", "entry-gamma"}[i]) {
			t.Fatalf("entry text leaked into the system message for entry %d", i)
		}
	}
}

// TestBatch_PerEntryIsolation is invariant 5: one bad entry (line-level
// + transport-level + reply-level) contaminates only its own row.
func TestBatch_PerEntryIsolation(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"good keep":   `{"verdict":"keep","reasons":["primary source"]}`,
		"bad verdict": `{"verdict":"meh","reasons":["no idea"]}`,
		"good drop":   `{"verdict":"drop","reasons":["promo"]}`,
	}, "transport boom")

	input := strings.Join([]string{
		`{"id":"a1","text":"good keep"}`,
		`{"id":"","text":"no id here"}`,       // line-level: missing id
		`{"id":"a3","text":"transport boom"}`, // transport-level: 401
		`{"id":"a4","text":"bad verdict"}`,    // reply-level: invalid verdict
		`{"id":"a5","text":"good drop"}`,
		`not json at all`, // line-level: invalid JSON
	}, "\n")

	res := jBatch(t, fb, input)

	if len(res.Rows) != 6 {
		t.Fatalf("rows = %d, want 6 (one per non-blank input line)", len(res.Rows))
	}
	for i, r := range res.Rows {
		t.Logf("row %d = %+v", i, r)
	}
	if res.Rows[0].Verdict != VerdictKeep || res.Rows[4].Verdict != VerdictDrop {
		t.Fatalf("good rows were contaminated: %+v / %+v", res.Rows[0], res.Rows[4])
	}
	for _, idx := range []int{1, 2, 3, 5} {
		if res.Rows[idx].Error == "" {
			t.Errorf("row %d must be an error row, got %+v", idx, res.Rows[idx])
		}
	}
	if !strings.Contains(res.Rows[1].Error, "missing id") {
		t.Errorf("row 1 error = %q", res.Rows[1].Error)
	}
	if !strings.Contains(res.Rows[2].Error, "judge call") {
		t.Errorf("row 2 error = %q", res.Rows[2].Error)
	}
	if !strings.Contains(res.Rows[3].Error, "malformed verdict") {
		t.Errorf("row 3 error = %q", res.Rows[3].Error)
	}
	if res.Rows[3].Verdict != "" {
		t.Errorf("an error row must never carry a verdict, got %q", res.Rows[3].Verdict)
	}

	// Failed calls burn no usage; the reply-level failure DID burn usage
	// (the provider answered). items == kept+dropped+gray+errors.
	if res.Meta.Items != 6 || res.Meta.Kept != 1 || res.Meta.Dropped != 1 || res.Meta.Gray != 0 {
		t.Fatalf("meta = %+v", res.Meta)
	}
	if res.Meta.PromptTokens != 3*fb.usage.PromptTokens {
		t.Errorf("prompt tokens = %d, want 3 calls counted (2 good + 1 malformed reply), got failures excluded",
			res.Meta.PromptTokens)
	}
	if res.Errors() != 4 {
		t.Errorf("error count = %d, want 4", res.Errors())
	}
}

func jBatch(t *testing.T, fb *fakeBackend, input string) *Result {
	t.Helper()
	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}
	return j.Batch(context.Background(), ParseEntries([]byte(input)))
}

// TestBatch_CancelledContextStillEmitsEveryEntry: cancellation stops
// new calls but must not truncate the output — every remaining entry
// still gets a complete error row (PRD §6 test bar: "半截输出必须是合法
// 的行").
func TestBatch_CancelledContextStillEmitsEveryEntry(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := j.Batch(ctx, ParseEntries([]byte(strings.Join([]string{
		`{"id":"a1","text":"one"}`,
		`{"id":"a2","text":"two"}`,
	}, "\n"))))

	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	for i, r := range res.Rows {
		if r.ID == "" || r.Error == "" {
			t.Fatalf("row %d = %+v, want id + error", i, r)
		}
		if !strings.Contains(r.Error, "not judged") {
			t.Errorf("row %d error = %q", i, r.Error)
		}
	}
	if res.Meta.Items != 2 || res.Meta.PromptTokens != 0 || res.Meta.CostUSD != 0 {
		t.Errorf("meta = %+v, want zero burn", res.Meta)
	}
}

// TestBatch_MidBatchCancellation: the context dies while the batch is
// running — judged rows keep their verdicts, unjudged rows are error
// rows, and the file is still complete.
func TestBatch_MidBatchCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fb := &fakeBackend{replies: map[string]string{
		"first": `{"verdict":"keep","reasons":["ok"]}`,
	}, usage: deepseek.Usage{PromptTokens: 10, CompletionTokens: 2}}
	var calls atomic.Int32
	fb.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The FIRST call serves normally; the SECOND cancels the context
		// before answering, i.e. the batch dies mid-run with row 0's
		// verdict already in hand.
		if calls.Add(1) >= 2 {
			cancel()
		}
		fb.handle(w, r)
	}))
	t.Cleanup(fb.srv.Close)

	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(ctx, ParseEntries([]byte(strings.Join([]string{
		`{"id":"a1","text":"first"}`,
		`{"id":"a2","text":"second"}`,
		`{"id":"a3","text":"third"}`,
	}, "\n"))))

	if len(res.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(res.Rows))
	}
	if res.Rows[0].Verdict != VerdictKeep {
		t.Fatalf("row 0 lost its verdict under cancellation: %+v", res.Rows[0])
	}
	for _, i := range []int{1, 2} {
		if res.Rows[i].Error == "" {
			t.Errorf("row %d must be an error row after cancellation, got %+v", i, res.Rows[i])
		}
		if res.Rows[i].Verdict != "" {
			t.Errorf("row %d must not carry a verdict, got %q", i, res.Rows[i].Verdict)
		}
	}
	if !strings.Contains(res.Rows[1].Error, "canceled") && !strings.Contains(res.Rows[1].Error, "not judged") {
		t.Errorf("row 1 error = %q, want a cancellation marker", res.Rows[1].Error)
	}
	if res.Meta.Kept != 1 || res.Meta.Items != 3 {
		t.Errorf("meta = %+v", res.Meta)
	}
}

// TestJudge_ConcurrentBatches is the -race check: one Judge is shared by
// concurrent callers (it holds no mutable state) and every batch keeps
// its own ordering.
func TestJudge_ConcurrentBatches(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"alpha": `{"verdict":"keep","reasons":["a"]}`,
		"beta":  `{"verdict":"drop","reasons":["b"]}`,
	}, "")
	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan string, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			in := []byte(fmt.Sprintf(`{"id":"g%d-a","text":"alpha"}`+"\n"+`{"id":"g%d-b","text":"beta"}`, g, g))
			res := j.Batch(context.Background(), ParseEntries(in))
			if len(res.Rows) != 2 || res.Meta.Items != 2 {
				errCh <- fmt.Sprintf("group %d: %+v", g, res.Meta)
				return
			}
			if res.Rows[0].ID != fmt.Sprintf("g%d-a", g) || res.Rows[1].ID != fmt.Sprintf("g%d-b", g) {
				errCh <- fmt.Sprintf("group %d: order/id lost: %+v", g, res.Rows)
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}

// ---- output contract ------------------------------------------------

// TestResult_JSONL_MetaIsLastLineAndRowsAreCompact pins the file shape:
// N row lines in input order, then exactly one `_meta` line, and every
// line decodes independently.
func TestResult_JSONL_MetaIsLastLineAndRowsAreCompact(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"x": `{"verdict":"keep","reasons":["cites <RFC> & details"]}`,
	}, "")
	j, err := New(fb.client(), "", testRubric, WithEffort("low"), WithTemperature(ptrTo(0.0)))
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte(`{"id":"a1","text":"x","meta":{"k":"v"}}`)))

	out, err := res.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (1 row + _meta); output:\n%s", len(lines), out)
	}

	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("row line is not valid JSON: %v\n%s", err, lines[0])
	}
	if row["id"] != "a1" || row["verdict"] != "keep" {
		t.Fatalf("row = %v", row)
	}
	meta, ok := row["meta"].(map[string]any)
	if !ok || meta["k"] != "v" {
		t.Fatalf("meta not round-tripped: %v", row["meta"])
	}
	if _, hasErr := row["error"]; hasErr {
		t.Fatalf("success row must not carry an error key: %v", row)
	}
	// EscapeHTML off: the reason's <RFC> survives unescaped (readable,
	// diff-stable), while remaining valid JSON.
	if !strings.Contains(lines[0], "<RFC>") {
		t.Fatalf("row line must keep <RFC> unescaped, got %s", lines[0])
	}

	var last map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("last line is not valid JSON: %v", err)
	}
	if _, ok := last["_meta"]; !ok {
		t.Fatalf("last line must be the _meta line, got %s", lines[len(lines)-1])
	}
	for k := range last {
		if k != "_meta" {
			t.Fatalf("_meta line must carry only the _meta key, got %q too", k)
		}
	}

	var metaWire Meta
	var env struct {
		Meta Meta `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &env); err != nil {
		t.Fatal(err)
	}
	metaWire = env.Meta
	if metaWire.Effort != "low" {
		t.Errorf("_meta effort = %q, want low (passed through)", metaWire.Effort)
	}
	if metaWire.Temperature == nil || *metaWire.Temperature != 0 {
		t.Errorf("_meta temperature = %v, want explicit 0", metaWire.Temperature)
	}
	if metaWire.Items != 1 || metaWire.Kept != 1 {
		t.Errorf("_meta counts = %+v", metaWire)
	}
}

// TestResult_JSONL_ErrorRowShape pins the error row: id + error only —
// no verdict, no reasons, no zero-valued noise.
func TestResult_JSONL_ErrorRowShape(t *testing.T) {
	res := &Result{
		Rows: []Row{
			{ID: "a1", Error: "model returned malformed verdict"},
			{ID: "a2", Verdict: VerdictDrop, Reasons: []string{"promo"}},
		},
		Meta: Meta{Items: 2, Dropped: 1},
	}
	out, err := res.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	if row["id"] != "a1" || row["error"] != "model returned malformed verdict" {
		t.Fatalf("error row = %v", row)
	}
	for _, bad := range []string{"verdict", "reasons", "signals", "meta"} {
		if _, ok := row[bad]; ok {
			t.Errorf("error row must omit %q, got %v", bad, row[bad])
		}
	}
	if !strings.Contains(lines[1], `"verdict":"drop"`) {
		t.Errorf("verdict row = %s", lines[1])
	}
}

// TestResult_JSONL_UnattributableLineKeepsIDKey: a line that could not
// be decoded has no id to echo, but the key stays present (empty) so the
// row shape stays uniform for consumers.
func TestResult_JSONL_UnattributableLineKeepsIDKey(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte("not json at all")))
	out, err := res.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	var row map[string]any
	if err := json.Unmarshal([]byte(first), &row); err != nil {
		t.Fatalf("row line is not JSON: %v (%s)", err, first)
	}
	if _, ok := row["id"]; !ok {
		t.Fatalf("row must still carry an id key, got %s", first)
	}
	if row["id"] != "" {
		t.Fatalf("id = %v, want empty (no id was attributable)", row["id"])
	}
	if _, ok := row["error"]; !ok {
		t.Fatalf("row must carry an error, got %s", first)
	}
	if res.Meta.Items != 1 || res.Errors() != 1 {
		t.Fatalf("meta/items = %d/%d", res.Meta.Items, res.Errors())
	}
}

// TestResult_JSONL_MarshalErrorSurfaces: a row carrying invalid raw
// JSON (possible only via a caller-built Row — the judge itself only
// stores raw bytes it decoded from the model) surfaces as an error
// instead of rendering a corrupt line.
func TestResult_JSONL_MarshalErrorSurfaces(t *testing.T) {
	res := &Result{
		Rows: []Row{{ID: "a1", Verdict: VerdictKeep,
			Signals: map[string]json.RawMessage{"bad": json.RawMessage("{not json")}}},
	}
	if _, err := res.JSONL(); err == nil {
		t.Fatal("expected a marshal error for an invalid raw signal")
	} else if !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("error should name the failing step, got %v", err)
	}
}

// TestResult_JSONL_EmptyBatchStillHasMeta: an empty input still emits
// the audit line — a caller reading cost/rubric fields must not need to
// special-case "nothing was judged".
func TestResult_JSONL_EmptyBatchStillHasMeta(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	j, err := New(fb.client(), "", testRubric)
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), nil)
	out, err := res.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), `{"_meta":`) {
		t.Fatalf("empty batch output = %q", out)
	}
	if res.Meta.Items != 0 || res.Meta.PromptTokens != 0 {
		t.Errorf("meta = %+v", res.Meta)
	}
	var env struct {
		Meta Meta `json:"_meta"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("empty-batch output must be valid JSONL: %v", err)
	}
	if len(env.Meta.RubricSHA256) != 64 {
		t.Errorf("rubric hash missing on empty batch: %+v", env.Meta)
	}
}

// ---- construction ---------------------------------------------------

func TestNew_Validation(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	cases := []struct {
		name    string
		client  Chatter
		rubric  string
		wantErr string
	}{
		{"nil client", nil, "r", "nil client"},
		{"empty rubric", fb.client(), "", "empty rubric"},
		{"blank rubric", fb.client(), "   \n\t ", "empty rubric"},
		{"ok", fb.client(), "r", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.client, "", c.rubric)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

// TestNew_Options: every knob lands on the wire exactly once and is
// echoed in _meta — whatever is set must be explained by the meta line
// (PRD §3.3 invariant 4).
func TestNew_Options(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"x": `{"verdict":"keep","reasons":["ok"]}`,
	}, "")
	temp := 0.0
	j, err := New(fb.client(), "fixture-model", testRubric,
		WithTemperature(&temp), WithEffort("low"), WithMaxTokens(512),
		WithTier(pricing.TierOffPeak))
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte(`{"id":"a1","text":"x"}`)))
	if res.Errors() != 0 {
		t.Fatalf("rows = %+v", res.Rows)
	}

	reqs := fb.snapshot()
	req := reqs[0]
	if req.Temperature == nil || *req.Temperature != 0 {
		t.Errorf("temperature = %v, want explicit 0", req.Temperature)
	}
	if req.ReasoningEffort != "low" {
		t.Errorf("reasoning_effort = %q", req.ReasoningEffort)
	}
	if req.MaxTokens != 512 {
		t.Errorf("max_tokens = %d", req.MaxTokens)
	}
	if req.Thinking == nil || req.Thinking.Type != "enabled" {
		t.Errorf("effort must enable thinking, got %+v", req.Thinking)
	}
	if res.Meta.Effort != "low" || res.Meta.Temperature == nil || *res.Meta.Temperature != 0 {
		t.Errorf("meta must echo the knobs: %+v", res.Meta)
	}
	if res.Meta.Model != "fixture-model" {
		t.Errorf("meta model = %q", res.Meta.Model)
	}

	// Off-peak tier halves DeepSeek's rate: cost must follow WithTier.
	wantOff := pricing.Cost("fixture-model", pricing.TierOffPeak, fb.usage)
	if diff := res.Meta.CostUSD - wantOff; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost = %v, want off-peak %v", res.Meta.CostUSD, wantOff)
	}
}

// TestNew_MaxTokens_Floors: a non-positive override must not produce a
// request the provider rejects.
func TestNew_MaxTokens_Floors(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	j, err := New(fb.client(), "", "r", WithMaxTokens(0), WithMaxTokens(-5))
	if err != nil {
		t.Fatal(err)
	}
	if j.maxTokens != defaultMaxTokens {
		t.Fatalf("maxTokens = %d, want default %d", j.maxTokens, defaultMaxTokens)
	}
}

// TestOne_NilResponseIsolated: a backend that returns (nil, nil) must
// not panic and must not abort the batch.
func TestBatch_NilResponseIsolated(t *testing.T) {
	j, err := New(nilResponseChatter{}, "", "r")
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte(`{"id":"a1","text":"x"}`)))
	if len(res.Rows) != 1 || !strings.Contains(res.Rows[0].Error, "empty response") {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

type nilResponseChatter struct{}

func (nilResponseChatter) Chat(context.Context, *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	return nil, nil
}

type noChoicesChatter struct{}

func (noChoicesChatter) Chat(context.Context, *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	return &deepseek.ChatResponse{Usage: deepseek.Usage{PromptTokens: 7}}, nil
}

// TestBatch_NoChoices: a well-formed envelope with zero choices is a
// reply-level failure, not a panic, and its usage still counts.
func TestBatch_NoChoices(t *testing.T) {
	j, err := New(noChoicesChatter{}, "", "r")
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte(`{"id":"a1","text":"x"}`)))
	if !strings.Contains(res.Rows[0].Error, "no choices") {
		t.Fatalf("row = %+v", res.Rows[0])
	}
	if res.Meta.PromptTokens != 7 {
		t.Errorf("tokens were burned by the provider and must be reported, got %d", res.Meta.PromptTokens)
	}
}

// ---- zero-state -----------------------------------------------------

// TestNoStateImports is the structural half of acceptance criterion ④
// (零状态): the kernel cannot write ~/.seek because it cannot reach a
// filesystem writer at all. An AST import whitelist — not a convention,
// not a "we promise not to" — so a future edit that pulls in os/paths/
// session fails here before it can ship.
func TestNoStateImports(t *testing.T) {
	files, err := parsePackageFiles()
	if err != nil {
		t.Skipf("AST scan unavailable: %v", err)
	}
	allowed := map[string]bool{
		"context":       true,
		"crypto/sha256": true,
		"encoding/hex":  true,
		"encoding/json": true,
		"errors":        true,
		"fmt":           true,
		"regexp":        true,
		"strings":       true,
		"unicode":       true,
		// Deliberately absent (the !allowed branch rejects them):
		// os / os/*, io, path/filepath, text/template, net/http, and any
		// seek package other than pricing — every one is a state door or
		// a second provider path.
		"github.com/whyiyhw/seek/pkg/deepseek":     true,
		"github.com/whyiyhw/seek/internal/pricing": true,
	}
	var banned []string
	for _, f := range files {
		if strings.HasSuffix(f.path, "_test.go") {
			continue
		}
		for _, imp := range f.imports {
			switch {
			case imp == "os" || strings.HasPrefix(imp, "os/"):
				banned = append(banned, f.path+": "+imp)
			case strings.HasPrefix(imp, "github.com/whyiyhw/seek/internal/") &&
				imp != "github.com/whyiyhw/seek/internal/pricing":
				banned = append(banned, f.path+": "+imp)
			case !allowed[imp]:
				banned = append(banned, f.path+": "+imp)
			}
		}
	}
	if len(banned) > 0 {
		t.Fatalf("internal/judge must stay zero-state / stdlib-only (no filesystem, no seek state packages):\n%s",
			strings.Join(banned, "\n"))
	}
}

// TestNoWriteCallSites is the behavioural belt to the import whitelist's
// braces: even with a permissible import, no non-test file may mention a
// filesystem write/mkdir API. Catches a future os.WriteFile smuggled in
// through an existing allowed import path.
func TestNoWriteCallSites(t *testing.T) {
	files, err := parsePackageFiles()
	if err != nil {
		t.Skipf("AST scan unavailable: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f.path, "_test.go") {
			continue
		}
		for _, needle := range []string{"os.Create", "os.WriteFile", "os.OpenFile", "os.Mkdir", "os.MkdirAll", "ioutil.WriteFile"} {
			if strings.Contains(f.src, needle) {
				t.Errorf("%s contains a write call site %q — the judge kernel is zero-state", f.path, needle)
			}
		}
	}
}

// ---- cost accounting -------------------------------------------------

// TestPriceUsage_Table: an omitted cache split is charged as MISS, never
// as free input — cost is an audit field and over-reporting is the safe
// direction.
func TestPriceUsage_Table(t *testing.T) {
	const model = "deepseek-flash"
	cases := []struct {
		name string
		u    deepseek.Usage
		// want is the usage actually priced: {hit, miss, completion}
		wantHit, wantMiss, wantCompletion int
	}{
		{
			name:           "cache split present",
			u:              deepseek.Usage{PromptTokens: 100, PromptCacheHitTokens: 90, PromptCacheMissTokens: 10, CompletionTokens: 5},
			wantHit:        90,
			wantMiss:       10,
			wantCompletion: 5,
		},
		{
			name:           "cache split omitted entirely",
			u:              deepseek.Usage{PromptTokens: 100, CompletionTokens: 5},
			wantHit:        0,
			wantMiss:       100,
			wantCompletion: 5,
		},
		{
			name:           "cache split partial (relay dropped the hit half)",
			u:              deepseek.Usage{PromptTokens: 100, PromptCacheMissTokens: 20, CompletionTokens: 5},
			wantHit:        0,
			wantMiss:       100,
			wantCompletion: 5,
		},
		{
			name:           "hit reported, miss omitted",
			u:              deepseek.Usage{PromptTokens: 100, PromptCacheHitTokens: 40, CompletionTokens: 5},
			wantHit:        40,
			wantMiss:       60,
			wantCompletion: 5,
		},
		{
			name:           "split exceeds prompt_tokens (provider inconsistency)",
			u:              deepseek.Usage{PromptTokens: 100, PromptCacheHitTokens: 120, PromptCacheMissTokens: 0, CompletionTokens: 5},
			wantHit:        120,
			wantMiss:       0,
			wantCompletion: 5,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := priceUsage(model, pricing.TierStandard, c.u)
			want := pricing.Cost(model, pricing.TierStandard, deepseek.Usage{
				PromptCacheHitTokens:  c.wantHit,
				PromptCacheMissTokens: c.wantMiss,
				CompletionTokens:      c.wantCompletion,
			})
			if diff := got - want; diff > 1e-12 || diff < -1e-12 {
				t.Fatalf("priceUsage = %v, want %v", got, want)
			}
		})
	}
}

// TestMeta_ReconcilesAgainstRows is the acceptance-criterion ① half:
// the aggregate must be auditable against the file itself, i.e.
// items == kept+dropped+gray+errorRows, and the token/cost fields must
// equal the sum of the rows actually judged.
func TestMeta_ReconcilesAgainstRows(t *testing.T) {
	fb := newFakeBackend(t, map[string]string{
		"k": `{"verdict":"keep","reasons":["r"]}`,
		"d": `{"verdict":"drop","reasons":["r"]}`,
		"g": `{"verdict":"gray","reasons":["r"]}`,
	}, "")
	input := strings.Join([]string{
		`{"id":"a1","text":"k"}`,
		`{"id":"a2","text":"d"}`,
		`{"id":"a3","text":"g"}`,
		`{"id":"","text":"no id"}`, // line error
		`{"id":"a5","text":"g"}`,   // judged
	}, "\n")
	res := jBatch(t, fb, input)

	out, err := res.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")

	// Recount straight from the rendered file, not from the struct —
	// that is what a caller can actually do.
	var kept, dropped, gray, errs int
	var lastEnv struct {
		Meta Meta `json:"_meta"`
	}
	for i, line := range lines {
		var row struct {
			Verdict string `json:"verdict"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
		if i == len(lines)-1 {
			if err := json.Unmarshal([]byte(line), &lastEnv); err != nil {
				t.Fatalf("last line is not a _meta envelope: %v", err)
			}
			continue
		}
		if row.Error != "" {
			errs++
			continue
		}
		switch row.Verdict {
		case VerdictKeep:
			kept++
		case VerdictDrop:
			dropped++
		case VerdictGray:
			gray++
		default:
			t.Errorf("line %d has neither a verdict nor an error: %s", i, line)
		}
	}

	m := lastEnv.Meta
	if m.Items != len(lines)-1 {
		t.Errorf("_meta.items = %d, want %d row lines", m.Items, len(lines)-1)
	}
	if m.Kept != kept || m.Dropped != dropped || m.Gray != gray {
		t.Errorf("_meta counts = %d/%d/%d, file says %d/%d/%d",
			m.Kept, m.Dropped, m.Gray, kept, dropped, gray)
	}
	if m.Items != kept+dropped+gray+errs {
		t.Errorf("items %d != kept %d + dropped %d + gray %d + errors %d",
			m.Items, kept, dropped, gray, errs)
	}
	// 4 judged calls burned usage (the 4th entry's line error burned none).
	if m.PromptTokens != 4*fb.usage.PromptTokens {
		t.Errorf("prompt_tokens = %d, want %d", m.PromptTokens, 4*fb.usage.PromptTokens)
	}
	if m.CacheHitTokens != 4*fb.usage.PromptCacheHitTokens {
		t.Errorf("cache_hit_tokens = %d, want %d", m.CacheHitTokens, 4*fb.usage.PromptCacheHitTokens)
	}
	wantCost := 4 * pricing.Cost(deepseek.ModelV41Flash, pricing.TierStandard, fb.usage)
	if diff := m.CostUSD - wantCost; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost = %v, want %v", m.CostUSD, wantCost)
	}
	if m.RubricSHA256 != sha256Hex(testRubric) {
		t.Errorf("rubric sha must be present on the reconciled file: %q", m.RubricSHA256)
	}
}

// TestMeta_TemperatureAndEffortAlwaysPresent pins the §8.1-4 audit
// requirement: the knobs are in _meta even when at their defaults, so
// two files can always be compared (null temperature = no wire key).
func TestMeta_TemperatureAndEffortAlwaysPresent(t *testing.T) {
	fb := newFakeBackend(t, nil, "")
	j, err := New(fb.client(), "", "r")
	if err != nil {
		t.Fatal(err)
	}
	res := j.Batch(context.Background(), ParseEntries([]byte(`{"id":"a1","text":"x"}`)))
	out, err := res.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	last := strings.TrimRight(string(out), "\n")
	if i := strings.LastIndexByte(last, '\n'); i >= 0 {
		last = last[i+1:]
	}
	for _, key := range []string{`"temperature":null`, `"effort":""`, `"model":"`, `"rubric_sha256":"`} {
		if !strings.Contains(last, key) {
			t.Errorf("_meta line must always carry %s, got %s", key, last)
		}
	}
}

// ---- helpers ---------------------------------------------------------

func ptrTo[T any](v T) *T { return &v }
