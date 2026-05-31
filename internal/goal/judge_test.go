package goal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestParseVerdict_Plain(t *testing.T) {
	v, err := parseVerdict(`{"met":true,"reason":"all tests pass","hint":""}`)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Met || v.Reason != "all tests pass" || v.Hint != "" {
		t.Fatalf("v = %+v", v)
	}
}

func TestParseVerdict_ToleratesFencesAndProse(t *testing.T) {
	content := "Sure:\n```json\n{\"met\": false, \"reason\": \"2 failing\", \"hint\": \"fix auth_test.go\"}\n```\nLet me know."
	v, err := parseVerdict(content)
	if err != nil {
		t.Fatal(err)
	}
	if v.Met || v.Reason != "2 failing" || v.Hint != "fix auth_test.go" {
		t.Fatalf("should extract object from fenced+prose reply: %+v", v)
	}
}

func TestParseVerdict_NoJSON(t *testing.T) {
	// A reply with no JSON object must ERROR — the driver then treats it as
	// not-met, so a malformed judge reply can never falsely report success.
	if _, err := parseVerdict("I think it's probably done"); err == nil {
		t.Fatal("reply with no JSON object should error")
	}
}

func TestParseVerdict_Malformed(t *testing.T) {
	if _, err := parseVerdict(`{"met": tru`); err == nil {
		t.Fatal("truncated/invalid JSON should error")
	}
}

// Full round-trip through a fake DeepSeek backend (seek's standard test
// pattern: httptest server + WithBaseURL — no real API key).
func TestDeepSeekJudge_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"{\"met\":true,\"reason\":\"tests green\",\"hint\":\"\"}"}}],"usage":{}}`)
	}))
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	j := NewDeepSeekJudge(c, "deepseek-flash")
	v, err := j.Judge(context.Background(), "all tests pass", TurnResult{Text: "ran go test, all green", ToolCalls: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Met || v.Reason != "tests green" {
		t.Fatalf("round-trip verdict = %+v", v)
	}
}

func TestDeepSeekJudge_NoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[],"usage":{}}`)
	}))
	defer srv.Close()

	c := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	j := NewDeepSeekJudge(c, "deepseek-flash")
	if _, err := j.Judge(context.Background(), "x", TurnResult{}); err == nil {
		t.Fatal("empty choices should error (driver then treats as not-met)")
	}
}
