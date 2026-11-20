package verifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestParseJudgement_ToleratesChrome: models add prose and fences
// despite "ONLY JSON" — extraction tolerates both, same as
// autopilot.parseTasks.
func TestParseJudgement_ToleratesChrome(t *testing.T) {
	rows, note, err := parseJudgement("Here you go:\n```json\n" + cannedJudgeReply + "\n```\nDone.")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Index != 1 || rows[0].Verdict != Pass {
		t.Fatalf("rows = %+v", rows)
	}
	if note != "ok" {
		t.Fatalf("note = %q", note)
	}
}

func TestParseJudgement_RejectsGarbage(t *testing.T) {
	if _, _, err := parseJudgement("no json here"); err == nil {
		t.Fatal("expected an error for json-less reply")
	}
	if _, _, err := parseJudgement("{broken"); err == nil {
		t.Fatal("expected an error for invalid json")
	}
}

// TestDeepSeekJudge_FreshContextSingleCall: the judge session is ONE
// chat call whose messages are exactly [system: SystemPrompt,
// user: bundle] — fresh context by construction, no history, no
// worker content.
func TestDeepSeekJudge_FreshContextSingleCall(t *testing.T) {
	var wire judgeWire
	srv := recordingJudgeServer(t, &wire)
	j := &DeepSeekJudge{Client: deepseek.New(deepseek.WithAPIKey("sk-test"), deepseek.WithBaseURL(srv.URL))}

	jd, err := j.Judge(context.Background(), "fixture-model", "BUNDLE-CONTENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(jd.Assertions) != 1 || jd.Tokens != 15 {
		t.Fatalf("judgement = %+v, want one row + 15 tokens", jd)
	}
	if wire.system != SystemPrompt {
		t.Fatal("system message must be the compile-time prompt")
	}
	if wire.user != "BUNDLE-CONTENT" {
		t.Fatal("user message must be the bundle verbatim")
	}
}

// TestDeepSeekJudge_HTTPFailureIsError: transport failures surface as
// errors (which Verify converts to doubt — see verifier_test).
func TestDeepSeekJudge_HTTPFailureIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails"}}`))
	}))
	t.Cleanup(srv.Close)
	j := &DeepSeekJudge{Client: deepseek.New(deepseek.WithAPIKey("sk-fake"), deepseek.WithBaseURL(srv.URL))}
	if _, err := j.Judge(context.Background(), "m", "bundle"); err == nil {
		t.Fatal("expected error on 401")
	} else if !strings.Contains(err.Error(), "judge call") {
		t.Fatalf("error should be wrapped, got %v", err)
	}
}

// TestDeepSeekJudge_NilClient: no backend wired → structured error,
// never a panic.
func TestDeepSeekJudge_NilClient(t *testing.T) {
	j := &DeepSeekJudge{}
	if _, err := j.Judge(context.Background(), "m", "b"); err == nil {
		t.Fatal("expected error with nil client")
	}
}
